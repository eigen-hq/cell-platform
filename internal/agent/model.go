package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Model is the client of the cell's model gateway (E-80). It speaks Chat
// Completions, which the gateway translates to every provider (E-93); this
// file alone knows that wire format, the loop works with message.
type Model struct {
	// URL is the gateway's base URL.
	URL string
	// Key is the agent's own key to the gateway, sent as a Bearer token (E-81).
	Key string
	// Name is the model the agent asks for; the gateway sends the one an
	// admin set.
	Name string
	HTTP *http.Client
	// Retries are the attempts after the first while the gateway is busy or
	// unreachable.
	Retries int
}

// message is one turn of a conversation.
type message struct {
	// Role is "system", "user", "assistant" or "tool".
	Role string
	Text string
	// Calls are an assistant's tool calls.
	Calls []toolCall
	// CallID is the call a tool message answers.
	CallID string
	// Reasoning and Signature are the provider's reasoning on an assistant
	// turn; they go back unchanged, since a provider may need them to go on.
	Reasoning, Signature string
}

type toolCall struct {
	ID, Name string
	Args     json.RawMessage
}

type wireMessage struct {
	Role               string     `json:"role"`
	Content            string     `json:"content"`
	ToolCalls          []wireCall `json:"tool_calls,omitempty"`
	ToolCallID         string     `json:"tool_call_id,omitempty"`
	ReasoningContent   string     `json:"reasoning_content,omitempty"`
	ReasoningSignature string     `json:"reasoning_signature,omitempty"`
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// complete asks the model for its next turn. stop is why the model ended
// it: "stop", "tool_calls", "length" or "content_filter".
func (m *Model) complete(ctx context.Context, msgs []message, tools []tool) (reply message, stop string, err error) {
	req := struct {
		Model     string        `json:"model"`
		Messages  []wireMessage `json:"messages"`
		Tools     []wireTool    `json:"tools,omitempty"`
		MaxTokens int           `json:"max_completion_tokens"`
		Effort    string        `json:"reasoning_effort"`
	}{Model: m.Name, MaxTokens: 16000, Effort: "high"}
	for _, msg := range msgs {
		w := wireMessage{Role: msg.Role, Content: msg.Text, ToolCallID: msg.CallID,
			ReasoningContent: msg.Reasoning, ReasoningSignature: msg.Signature}
		for _, c := range msg.Calls {
			wc := wireCall{ID: c.ID, Type: "function"}
			wc.Function.Name, wc.Function.Arguments = c.Name, string(c.Args)
			w.ToolCalls = append(w.ToolCalls, wc)
		}
		req.Messages = append(req.Messages, w)
	}
	for _, t := range tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name, wt.Function.Description, wt.Function.Parameters = t.Name, t.Description, t.Params
		req.Tools = append(req.Tools, wt)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return message{}, "", err
	}
	out, err := m.post(ctx, body)
	if err != nil {
		return message{}, "", err
	}
	var res struct {
		Choices []struct {
			Message      wireMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return message{}, "", fmt.Errorf("answer of the gateway: %w", err)
	}
	if len(res.Choices) == 0 {
		return message{}, "", errors.New("answer of the gateway without a choice")
	}
	w := res.Choices[0].Message
	reply = message{Role: "assistant", Text: w.Content, Reasoning: w.ReasoningContent, Signature: w.ReasoningSignature}
	for _, c := range w.ToolCalls {
		args := json.RawMessage(c.Function.Arguments)
		if strings.TrimSpace(c.Function.Arguments) == "" {
			args = json.RawMessage("{}")
		}
		reply.Calls = append(reply.Calls, toolCall{ID: c.ID, Name: c.Function.Name, Args: args})
	}
	return reply, res.Choices[0].FinishReason, nil
}

// post sends one request, again while the gateway is busy or unreachable.
// It does not wait on the cell's token limit: the gateway's 429 asks to
// wait until the hour refills (E-81), which would hold the task that long.
func (m *Model) post(ctx context.Context, body []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.URL, "/")+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.Key)
		wait := time.Duration(500<<attempt) * time.Millisecond
		retry := true
		res, err := m.HTTP.Do(req)
		if err == nil {
			out, rerr := io.ReadAll(res.Body)
			_ = res.Body.Close()
			switch {
			case rerr != nil:
				err = rerr
			case res.StatusCode == http.StatusOK:
				return out, nil
			default:
				err = &statusError{Code: res.StatusCode, Header: res.Header, Body: out}
				retry = res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= http.StatusInternalServerError
				if s, perr := strconv.Atoi(res.Header.Get("Retry-After")); perr == nil {
					wait, retry = time.Duration(s)*time.Second, retry && s <= 60
				}
			}
		}
		if !retry || attempt >= m.Retries {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// statusError is an answer of the gateway other than 200.
type statusError struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (e *statusError) Error() string {
	body := string(e.Body)
	if len(body) > 2000 {
		body = body[:2000]
	}
	return fmt.Sprintf("HTTP %d: %s", e.Code, strings.TrimSpace(body))
}

// modelError explains the cell's token limit: the gateway's 429 with none
// of its tokens left. A provider's 429 carries the gateway's headers too,
// with tokens left; it and any other error stay as they are.
func modelError(err error) string {
	var e *statusError
	if errors.As(err, &e) && e.Code == http.StatusTooManyRequests {
		h := e.Header
		if limit := h.Get("X-Ratelimit-Limit"); limit != "" && h.Get("X-Ratelimit-Remaining") == "0" {
			wait := "later"
			if s, perr := strconv.Atoi(h.Get("X-Ratelimit-Reset")); perr == nil {
				wait = fmt.Sprintf("in %d minutes", (s+59)/60)
			}
			return fmt.Sprintf("the cell's limit of %s tokens per hour is used up; ask again %s, or raise the limit under Organization, Models", limit, wait)
		}
	}
	return err.Error()
}
