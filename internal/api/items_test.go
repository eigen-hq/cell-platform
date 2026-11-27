package api

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/store"
)

// Agents record the transcripts of their tasks (E-98): only in a task they
// work on, at positions that make a repeated call harmless, and only items
// of the four kinds. Who may read the task reads its transcript.
func TestTranscripts(t *testing.T) {
	running := func(name, uid, assignee string) *cellv1.Task {
		tk := pendingTask(name)
		tk.UID = types.UID(uid)
		tk.Status = cellv1.TaskStatus{Phase: cellv1.RunRunning, Assignee: assignee}
		return tk
	}
	other := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "other", UID: "other-1"},
		Spec: cellv1.AgentSpec{Kinds: []string{"package"}, Apps: []string{"*"}, Tools: []string{"*"}}}
	h := newHarness(t, running("notes-diagnose-1", "u-1", opsAccount), running("notes-diagnose-2", "u-2", "cell-agent-other.other-1"), other)
	h.agentTask = ""
	path := "/v0/tasks/notes-diagnose-1/items"
	msg := func(role, text string) map[string]any {
		return map[string]any{"type": "message", "role": role, "parts": []any{map[string]any{"mediaType": "text/plain", "text": text}}}
	}
	call := map[string]any{"type": "function_call", "callId": "c1", "name": "getApp", "arguments": `{"name":`}
	output := map[string]any{"type": "function_call_output", "callId": "c1", "output": "ok"}

	if code, _ := h.do("POST", path, "agent", map[string]any{"from": 0, "items": []any{msg("user", "brief")}}); code != 503 {
		t.Errorf("without a database: %d", code)
	}
	h.server.Transcripts = &store.Memory{}
	for i, tc := range []struct {
		path  string
		from  int
		items []any
		code  int
	}{
		// Arguments the model cut are kept as it sent them.
		{path, 0, []any{msg("user", "brief"), call}, 204},
		{path, 0, []any{msg("user", "brief"), call}, 204},
		{path, 1, []any{msg("assistant", "not kept"), output}, 204},
		{path, 5, []any{msg("assistant", "after a gap")}, 409},
		{path, 3, []any{msg("system", "x")}, 400},
		{path, 3, []any{map[string]any{"type": "message", "role": "user"}}, 400},
		{path, 3, []any{map[string]any{"type": "function_call", "name": "x"}}, 400},
		{path, 3, []any{map[string]any{"type": "reasoning"}}, 400},
		{path, 3, []any{map[string]any{"type": "image"}}, 400},
		{path, 3, []any{map[string]any{"type": "function_call_output", "callId": "c2", "output": strings.Repeat("x", maxItemSize)}}, 400},
		{path, 3, []any{}, 400},
		{path, maxItems, []any{msg("user", "x")}, 400},
		{"/v0/tasks/notes-diagnose-2/items", 0, []any{msg("user", "brief")}, 409},
	} {
		if code, body := h.do("POST", tc.path, "agent", map[string]any{"from": tc.from, "items": tc.items}); code != tc.code {
			t.Errorf("case %d: %d %v, want %d", i, code, body, tc.code)
		}
	}

	code, body := h.do("GET", path, "admin", nil)
	items, _ := body.([]any)
	if code != 200 || len(items) != 3 || field(items[1], "item", "name") != "getApp" || field(items[2], "item", "output") != "ok" ||
		field(items[2], "seq") != 2.0 {
		t.Errorf("transcript: %d %v", code, body)
	}
	if code, _ := h.do("GET", path, "agent", nil); code != 200 {
		t.Errorf("the agent reads its task's transcript: %d", code)
	}
	if code, body := h.do("GET", "/v0/tasks/notes-diagnose-2/items", "admin", nil); code != 200 || len(body.([]any)) != 0 {
		t.Errorf("empty transcript: %d %v", code, body)
	}
	for _, token := range []string{"viewer", "agent-other"} {
		if code, _ := h.do("GET", path, token, nil); code != 403 {
			t.Errorf("%s reads the transcript: %d", token, code)
		}
	}
}
