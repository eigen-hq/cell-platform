package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/store"
)

// Transcripts keeps the items of task transcripts in the platform database
// (E-98); a task is named by its UID, so a new task of the same name starts
// empty.
type Transcripts interface {
	Append(ctx context.Context, task string, from int, items []json.RawMessage) error
	Items(ctx context.Context, task string) ([]store.Item, error)
}

// The bounds of a transcript: a task has at most 200 steps and 40 model
// turns, and a tool result reaches the model with at most 30000 bytes.
const (
	maxItems    = 2000
	maxItemSize = 256 << 10
)

// item is one item of a transcript, named after Open Responses
// (architecture.md, Agent harness): a message, a tool call and its output,
// or the provider's reasoning, passed back unchanged.
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

// check refuses an item that lacks what its kind needs. Fields of other
// kinds are kept but mean nothing.
func (it item) check() error {
	switch it.Type {
	case "message":
		if it.Role != "user" && it.Role != "assistant" {
			return errors.New("a message has role user or assistant")
		}
		if len(it.Parts) == 0 {
			return errors.New("a message has parts")
		}
		for _, p := range it.Parts {
			if p.MediaType != "text/plain" || p.Text == "" {
				return errors.New("a part is text/plain with text")
			}
		}
	case "function_call":
		if it.CallID == "" || it.Name == "" {
			return errors.New("a function_call has a callId and a name")
		}
	case "function_call_output":
		if it.CallID == "" {
			return errors.New("a function_call_output has a callId")
		}
	case "reasoning":
		if it.Text == "" && it.Signature == "" {
			return errors.New("a reasoning item has a text or a signature")
		}
	default:
		return errors.New("type must be message, function_call, function_call_output or reasoning")
	}
	return nil
}

type appendItemsRequest struct {
	// From is the position of the first item: an agent sends it again
	// after a lost answer, and the positions stored already stay.
	From  int    `json:"from"`
	Items []item `json:"items"`
}

func (s *Server) transcripts() (Transcripts, error) {
	if s.Transcripts == nil {
		return nil, errorf(http.StatusServiceUnavailable, "the cell has no platform database")
	}
	return s.Transcripts, nil
}

// transcriptOf is the key of task's transcript.
func transcriptOf(t *cellv1.Task) (string, error) {
	if t.UID == "" {
		return "", errors.New("task " + t.Name + " has no UID")
	}
	return string(t.UID), nil
}

// appendItems records items of the transcript of the task the caller is
// working on.
func (s *Server) appendItems(w http.ResponseWriter, r *http.Request, c *Caller) error {
	ts, err := s.transcripts()
	if err != nil {
		return err
	}
	var req appendItemsRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if len(req.Items) == 0 || req.From < 0 || req.From+len(req.Items) > maxItems {
		return errorf(http.StatusBadRequest, "send 1 or more items at positions 0 to %d", maxItems-1)
	}
	raw := make([]json.RawMessage, len(req.Items))
	for i, it := range req.Items {
		if err := it.check(); err != nil {
			return errorf(http.StatusBadRequest, "item %d: %v", i, err)
		}
		if raw[i], err = json.Marshal(it); err != nil {
			return err
		}
		if len(raw[i]) > maxItemSize {
			return errorf(http.StatusBadRequest, "item %d has %d bytes, at most %d", i, len(raw[i]), maxItemSize)
		}
	}
	t, err := s.task(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if t.Status.Phase != cellv1.RunRunning || t.Status.Assignee != c.Username {
		return errorf(http.StatusConflict, "task %s is not running for you (phase %s, assignee %q)", t.Name, t.Status.Phase, t.Status.Assignee)
	}
	key, err := transcriptOf(t)
	if err != nil {
		return err
	}
	if err := ts.Append(r.Context(), key, req.From, raw); err != nil {
		if errors.Is(err, store.ErrGap) {
			return errorf(http.StatusConflict, "task %s: %v", t.Name, err)
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// listItems is a task's transcript, for whoever may read the task.
func (s *Server) listItems(w http.ResponseWriter, r *http.Request, c *Caller) error {
	ts, err := s.transcripts()
	if err != nil {
		return err
	}
	t, err := s.task(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if !agentSees(r.Context(), c, t) {
		return mayTake(agentOf(r.Context()), t.Spec.Kind, t.Spec.App)
	}
	key, err := transcriptOf(t)
	if err != nil {
		return err
	}
	items, err := ts.Items(r.Context(), key)
	if err != nil {
		return err
	}
	if items == nil {
		items = []store.Item{}
	}
	writeJSON(w, http.StatusOK, items)
	return nil
}
