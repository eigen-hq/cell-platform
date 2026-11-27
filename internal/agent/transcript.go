package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// item is one item of a task's transcript in the platform API (E-98),
// named after Open Responses.
type item struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Parts     []part `json:"parts,omitempty"`
	CallID    string `json:"callId,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type part struct {
	MediaType string `json:"mediaType"`
	Text      string `json:"text"`
}

// itemsOf is a turn as items: an assistant's turn is its reasoning, its
// text and its calls, in that order.
func itemsOf(m message) []item {
	text := func(role string) item {
		return item{Type: "message", Role: role, Parts: []part{{MediaType: "text/plain", Text: m.Text}}}
	}
	switch m.Role {
	case "user":
		return []item{text("user")}
	case "tool":
		return []item{{Type: "function_call_output", CallID: m.CallID, Output: m.Text}}
	case "assistant":
		var out []item
		if m.Reasoning != "" || m.Signature != "" {
			out = append(out, item{Type: "reasoning", Text: m.Reasoning, Signature: m.Signature})
		}
		if m.Text != "" {
			out = append(out, text("assistant"))
		}
		for _, c := range m.Calls {
			out = append(out, item{Type: "function_call", CallID: c.ID, Name: c.Name, Arguments: string(c.Args)})
		}
		return out
	}
	return nil
}

// transcript records a task's conversation as it goes. The system prompt is
// not part of it: it is the agent's own, the same for every task.
type transcript struct {
	a    *Agent
	path string
	next int
}

// postSize bounds the items of one post: the API reads at most 1 MiB, and
// one turn's outputs can be more.
const postSize = 512 << 10

// add records turns, in posts of at most postSize. The positions make a
// repeated call harmless, so it tries again for about a minute while the
// API fails.
func (tr *transcript) add(ctx context.Context, turns ...message) error {
	var items []item
	for _, m := range turns {
		items = append(items, itemsOf(m)...)
	}
	for len(items) > 0 {
		n, size := 0, 0
		for ; n < len(items); n++ {
			b, err := json.Marshal(items[n])
			if err != nil {
				return err
			}
			if n > 0 && size+len(b) > postSize {
				break
			}
			size += len(b)
		}
		if err := tr.a.send(ctx, tr.path, map[string]any{"from": tr.next, "items": items[:n]}, http.StatusNoContent, 7); err != nil {
			return fmt.Errorf("transcript: %w", err)
		}
		tr.next += n
		items = items[n:]
	}
	return nil
}

// send posts in until the API answers ok, again while it is unreachable or
// failing (5xx), with a pause that doubles up to a minute. tries bounds the
// attempts; 0 tries forever.
func (a *Agent) send(ctx context.Context, path string, in any, ok, tries int) error {
	delay := time.Second
	for attempt := 1; ; attempt++ {
		code, body, err := a.API.Do(ctx, http.MethodPost, path, "", in)
		if err == nil && code == ok {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("HTTP %d: %s", code, body)
			if code < http.StatusInternalServerError {
				return fmt.Errorf("POST %s: %w", path, err)
			}
		}
		if attempt == tries {
			return fmt.Errorf("POST %s: %w", path, err)
		}
		a.Log.Warn("retrying", "call", "POST "+path, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, time.Minute)
	}
}
