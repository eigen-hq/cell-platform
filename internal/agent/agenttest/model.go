// Package agenttest stands in for the language model in tests: an HTTP
// server that answers Chat Completions with the turns a script returns.
// The script sees the real tool results, so a test can check that the
// agent hands the model what the platform reports.
package agenttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Call is a tool call of the model.
type Call struct {
	Tool  string
	Input any
}

// Turn is the model's answer: tool calls, or (without calls) the final text.
type Turn struct {
	Calls []Call
	Text  string
	// Finish is the finish reason; empty means "tool_calls" with calls and
	// "stop" without.
	Finish string
}

// Conversation is what the model got so far.
type Conversation struct {
	// Task is the first user message.
	Task string
	// Results are the tool results since the model's last turn; errors
	// start with "ERROR ".
	Results []string
	// Notes are the agent's messages after the task since the model's last
	// turn.
	Notes []string
	// Turn counts the model's earlier answers.
	Turn int
	// Tools are the names of the tools the agent offers.
	Tools []string
}

// Script returns the model's next turn.
type Script func(Conversation) (Turn, error)

type request struct {
	Messages []struct {
		Role               string          `json:"role"`
		Content            json.RawMessage `json:"content"`
		ReasoningContent   string          `json:"reasoning_content"`
		ReasoningSignature string          `json:"reasoning_signature"`
		ToolCalls          []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

// Serve starts the model. Failures of the script, a turn whose reasoning
// the agent did not hand back unchanged, and a tool call without its result
// answer with HTTP 500 and are kept for Errors.
func Serve(script Script) *Model {
	m := &Model{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		turn, err := m.answer(r, script)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			m.mu.Lock()
			m.errs = append(m.errs, err)
			m.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "server_error", "message": err.Error()}})
			return
		}
		_ = json.NewEncoder(w).Encode(turn)
	}))
	return m
}

// Model is a scripted model server.
type Model struct {
	*httptest.Server
	mu   sync.Mutex
	n    int
	errs []error
}

// Errors are the script's failures so far.
func (m *Model) Errors() []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]error(nil), m.errs...)
}

// reasoning is what the model thought on its turn n; the agent must send
// it back with that turn.
func reasoning(n int) (text, signature string) {
	return fmt.Sprintf("thinking on turn %d", n), fmt.Sprintf("sig-%d", n)
}

func (m *Model) answer(r *http.Request, script Script) (map[string]any, error) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	var c Conversation
	for _, t := range req.Tools {
		c.Tools = append(c.Tools, t.Function.Name)
	}
	// Providers refuse a turn whose tool calls are not all answered right
	// after it.
	open := map[string]bool{}
	for i, msg := range req.Messages {
		text, err := content(msg.Content)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		if msg.Role != "tool" && len(open) > 0 {
			return nil, fmt.Errorf("message %d: tool calls %v without a result", i, open)
		}
		switch msg.Role {
		case "assistant":
			if want, sig := reasoning(c.Turn); msg.ReasoningContent != want || msg.ReasoningSignature != sig {
				return nil, fmt.Errorf("turn %d came back with reasoning %q, signature %q", c.Turn, msg.ReasoningContent, msg.ReasoningSignature)
			}
			c.Turn++
			c.Results, c.Notes = nil, nil
			for _, call := range msg.ToolCalls {
				open[call.ID] = true
			}
		case "user":
			if c.Task == "" {
				c.Task = text
			} else {
				c.Notes = append(c.Notes, text)
			}
		case "tool":
			if !open[msg.ToolCallID] {
				return nil, fmt.Errorf("message %d: result for call %q, which is not open", i, msg.ToolCallID)
			}
			delete(open, msg.ToolCallID)
			c.Results = append(c.Results, text)
		}
	}
	if len(open) > 0 {
		return nil, fmt.Errorf("tool calls %v without a result", open)
	}
	if c.Task == "" {
		return nil, fmt.Errorf("no user message")
	}
	turn, err := script(c)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.n++
	id := m.n
	m.mu.Unlock()
	text, sig := reasoning(c.Turn)
	msg := map[string]any{"role": "assistant", "content": nil, "reasoning_content": text, "reasoning_signature": sig}
	if turn.Text != "" {
		msg["content"] = turn.Text
	}
	finish := "stop"
	var calls []map[string]any
	for i, call := range turn.Calls {
		args, err := json.Marshal(call.Input)
		if err != nil {
			return nil, err
		}
		calls = append(calls, map[string]any{"id": fmt.Sprintf("call_%d_%d", id, i), "type": "function",
			"function": map[string]any{"name": call.Tool, "arguments": string(args)}})
		finish = "tool_calls"
	}
	if calls != nil {
		msg["tool_calls"] = calls
	}
	if turn.Finish != "" {
		finish = turn.Finish
	}
	return map[string]any{
		"id": fmt.Sprintf("chatcmpl-%d", id), "object": "chat.completion", "model": "scripted",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}, nil
}

// content is a message's text: a string, or the text of its parts.
func content(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", err
	}
	for _, p := range parts {
		s += p.Text
	}
	return s, nil
}
