// Package agenttest stands in for the language model in tests: an HTTP
// server that answers the Messages API with the turns a script returns.
// The script sees the real tool results, so a test can check that the
// agent hands the model what the platform reports.
package agenttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
}

// Conversation is what the model got so far.
type Conversation struct {
	// Task is the first user message.
	Task string
	// Results are the tool results of the last user message; errors start
	// with "ERROR ".
	Results []string
	// Turn counts the model's earlier answers.
	Turn int
	// Tools are the names of the tools the agent offers.
	Tools []string
}

// Script returns the model's next turn.
type Script func(Conversation) (Turn, error)

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

type request struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

// Serve starts the model. Failures of the script answer with HTTP 500 and
// are kept for Errors.
func Serve(script Script) *Model {
	m := &Model{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		turn, err := m.answer(r, script)
		if err != nil {
			m.mu.Lock()
			m.errs = append(m.errs, err)
			m.mu.Unlock()
			http.Error(w, `{"type":"error","error":{"type":"api_error","message":"`+strings.ReplaceAll(err.Error(), `"`, `'`)+`"}}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
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

func (m *Model) answer(r *http.Request, script Script) (map[string]any, error) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("no messages")
	}
	var c Conversation
	for _, t := range req.Tools {
		c.Tools = append(c.Tools, t.Name)
	}
	for i, msg := range req.Messages {
		var blocks []block
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			var s string
			if json.Unmarshal(msg.Content, &s) != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			blocks = []block{{Type: "text", Text: s}}
		}
		switch {
		case msg.Role == "assistant":
			c.Turn++
		case i == 0:
			for _, b := range blocks {
				c.Task += b.Text
			}
		case i == len(req.Messages)-1:
			for _, b := range blocks {
				if b.Type == "tool_result" {
					c.Results = append(c.Results, resultText(b))
				}
			}
		}
	}
	turn, err := script(c)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.n++
	id := m.n
	m.mu.Unlock()
	content := []map[string]any{}
	if turn.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": turn.Text})
	}
	stop := "end_turn"
	for i, call := range turn.Calls {
		content = append(content, map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d_%d", id, i), "name": call.Tool, "input": call.Input})
		stop = "tool_use"
	}
	return map[string]any{
		"id": fmt.Sprintf("msg_%d", id), "type": "message", "role": "assistant", "model": "scripted",
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	}, nil
}

func resultText(b block) string {
	var text string
	var parts []block
	if json.Unmarshal(b.Content, &text) != nil && json.Unmarshal(b.Content, &parts) == nil {
		for _, p := range parts {
			text += p.Text
		}
	}
	if b.IsError {
		return "ERROR " + text
	}
	return text
}
