package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/store"
)

// Agents record the transcripts of their tasks (E-98): only in a task they
// work on, at positions that make a repeated call harmless, and only items
// of the four kinds. People who may read the task read its transcript, an
// agent only the transcripts of its own tasks.
func TestTranscripts(t *testing.T) {
	running := func(name, uid, assignee string) *cellv1.Task {
		tk := pendingTask(name)
		tk.UID = types.UID(uid)
		tk.Status = cellv1.TaskStatus{Phase: cellv1.RunRunning, Assignee: assignee}
		return tk
	}
	other := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "other", UID: "other-1"},
		Spec: cellv1.AgentSpec{Kinds: []string{"diagnose", "package"}, Apps: []string{"*"}, Tools: []string{"*"}}}
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
		// Postgres refuses NUL in jsonb; an app's log may hold one.
		{path, 3, []any{map[string]any{"type": "function_call_output", "callId": "c2", "output": "a\x00b"}}, 204},
		{path, maxItems, []any{msg("user", "x")}, 400},
		{"/v0/tasks/notes-diagnose-2/items", 0, []any{msg("user", "brief")}, 409},
	} {
		if code, body := h.do("POST", tc.path, "agent", map[string]any{"from": tc.from, "items": tc.items}); code != tc.code {
			t.Errorf("case %d: %d %v, want %d", i, code, body, tc.code)
		}
	}

	code, body := h.do("GET", path, "admin", nil)
	items, _ := body.([]any)
	if code != 200 || len(items) != 4 || field(items[1], "item", "name") != "getApp" || field(items[2], "item", "output") != "ok" ||
		field(items[2], "seq") != 2.0 || field(items[3], "item", "output") != "a\uFFFDb" {
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

// A person replies to the agent of a task (E-99): only an admin, only when
// the agent has answered or asked, and only while that agent can take the
// task again. The task is then pending for that agent alone, which may also
// end its next turn with a question.
func TestReply(t *testing.T) {
	task := func(name string, phase cellv1.RunPhase, assignee string) *cellv1.Task {
		tk := pendingTask(name)
		tk.UID = types.UID("u-" + name)
		tk.Status = cellv1.TaskStatus{Phase: phase, Assignee: assignee}
		return tk
	}
	full := task("full", cellv1.RunSucceeded, opsAccount)
	for range maxSteps {
		full.Status.Steps = append(full.Status.Steps, cellv1.TaskStep{At: metav1.Now(), Call: "GET /v0/apps/notes"})
	}
	tasks := []*cellv1.Task{task("asked", cellv1.RunWaitingForInput, opsAccount), task("done", cellv1.RunSucceeded, opsAccount),
		task("failed", cellv1.RunFailed, opsAccount), task("running", cellv1.RunRunning, opsAccount), task("pending", cellv1.RunPending, ""),
		task("gone", cellv1.RunSucceeded, "cell-agent-gone.gone-1"), task("again", cellv1.RunSucceeded, "cell-agent-ops.ops-0"),
		task("deleting", cellv1.RunSucceeded, "cell-agent-old.old-1"), task("narrow", cellv1.RunSucceeded, "cell-agent-narrow.narrow-1"),
		task("person", cellv1.RunSucceeded, "admin-user"), full, task("empty", cellv1.RunSucceeded, opsAccount)}
	objs := []client.Object{
		&cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "old", UID: "old-1", DeletionTimestamp: &metav1.Time{Time: time.Now()}, Finalizers: []string{"x"}},
			Spec: cellv1.AgentSpec{Kinds: []string{"diagnose"}, Apps: []string{"*"}, Tools: []string{"*"}}},
		&cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "narrow", UID: "narrow-1"}, Spec: cellv1.AgentSpec{Kinds: []string{"package"}, Apps: []string{"*"}}},
		&cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "other", UID: "other-1"}, Spec: cellv1.AgentSpec{Kinds: []string{"diagnose"}, Apps: []string{"*"}}},
	}
	for _, tk := range tasks {
		objs = append(objs, tk)
	}
	h := newHarness(t, objs...)
	h.agentTask = ""
	reply := func(name, token, text string) (int, any) {
		h.t.Helper()
		return h.do("POST", "/v0/tasks/"+name+"/reply", token, map[string]any{"text": text})
	}
	if code, _ := reply("asked", "admin", "2.0"); code != 503 {
		t.Errorf("without a database: %d", code)
	}
	ts := &store.Memory{}
	h.server.Transcripts = ts
	brief := []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","parts":[{"mediaType":"text/plain","text":"brief"}]}`)}
	for _, tk := range tasks {
		if tk.Name != "empty" {
			if err := ts.Append(context.Background(), string(tk.UID), 0, brief); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, tc := range []struct {
		task, token, text string
		code              int
	}{
		{"asked", "viewer", "2.0", 403},
		{"asked", "agent", "2.0", 403},
		{"asked", "admin", " ", 400},
		{"asked", "admin", strings.Repeat("x", maxAsk+1), 400},
		{"running", "admin", "And?", 409},
		{"pending", "admin", "And?", 409},
		{"gone", "admin", "And?", 409},
		{"again", "admin", "And?", 409},
		{"deleting", "admin", "And?", 409},
		{"narrow", "admin", "And?", 409},
		{"person", "admin", "And?", 409},
		{"full", "admin", "And?", 409},
		{"empty", "admin", "And?", 409},
		{"missing", "admin", "And?", 404},
		{"done", "admin", "And its port?", 200},
		{"failed", "admin", "Try again.", 200},
		{"asked", "admin", " 2.0 ", 200},
	} {
		if code, body := reply(tc.task, tc.token, tc.text); code != tc.code {
			t.Errorf("reply to %s as %s: %d %v, want %d", tc.task, tc.token, code, body, tc.code)
		}
	}
	code, body := h.do("GET", "/v0/tasks/asked", "admin", nil)
	if code != 200 || field(body, "phase") != "Pending" || field(body, "assignee") != opsAccount || field(body, "finishedAt") != nil ||
		field(body, "reply", "by") != "admin-user" || field(body, "reply", "text") != "2.0" {
		t.Errorf("replied task: %d %v", code, body)
	}

	// Only the task's agent takes the reply; its next turn may ask again.
	if code, body := h.do("POST", "/v0/tasks/asked/claim", "agent-other", nil); code != 409 || !strings.Contains(fmt.Sprint(field(body, "error")), "waits for") {
		t.Errorf("another agent claims the reply: %d %v", code, body)
	}
	if code, body := h.do("POST", "/v0/tasks/asked/claim", "agent", nil); code != 200 || field(body, "phase") != "Running" {
		t.Fatalf("claim the reply: %d %v", code, body)
	}
	if code, _ := h.do("POST", "/v0/tasks/asked/finish", "agent", map[string]any{"result": "Which?", "failed": true, "waiting": true}); code != 400 {
		t.Errorf("fail and wait: %d", code)
	}
	if code, body := h.do("POST", "/v0/tasks/asked/finish", "agent", map[string]any{"result": "Which one?", "waiting": true}); code != 200 ||
		field(body, "phase") != "WaitingForInput" || field(body, "result") != "Which one?" {
		t.Errorf("ask again: %d %v", code, body)
	}
}

// An agent narrowed after a reply came for it (E-99): its claim fails the
// task instead of leaving it pending, so the reply waits for nobody and the
// agent goes on with the tasks it takes.
func TestReplyNarrowed(t *testing.T) {
	late := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "late", UID: "late-1"}, Spec: cellv1.AgentSpec{Kinds: []string{"diagnose", "package"}, Apps: []string{"*"}}}
	done := pendingTask("done")
	done.UID = "u-done"
	done.Status = cellv1.TaskStatus{Phase: cellv1.RunSucceeded, Assignee: "cell-agent-late.late-1"}
	next := pendingTask("next")
	next.Spec.Kind = "package"
	h := newHarness(t, late, done, next)
	h.agentTask = ""
	ts := &store.Memory{}
	h.server.Transcripts = ts
	if err := ts.Append(context.Background(), "u-done", 0, []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","parts":[{"mediaType":"text/plain","text":"brief"}]}`)}); err != nil {
		t.Fatal(err)
	}
	if code, body := h.do("POST", "/v0/tasks/done/reply", "admin", map[string]any{"text": "And?"}); code != 200 {
		t.Fatalf("reply: %d %v", code, body)
	}
	if err := h.kube.Get(context.Background(), client.ObjectKeyFromObject(late), late); err != nil {
		t.Fatal(err)
	}
	late.Spec.Kinds = []string{"package"}
	if err := h.kube.Update(context.Background(), late); err != nil {
		t.Fatal(err)
	}
	if code, body := h.do("POST", "/v0/tasks/done/claim", "agent-late", nil); code != 409 {
		t.Errorf("claim the reply after narrowing: %d %v", code, body)
	}
	if code, body := h.do("GET", "/v0/tasks/done", "admin", nil); code != 200 || field(body, "phase") != "Failed" ||
		!strings.Contains(fmt.Sprint(field(body, "result")), "takes no diagnose task") || field(body, "finishedAt") == nil {
		t.Errorf("the task the agent no longer takes: %d %v", code, body)
	}
	if code, body := h.do("POST", "/v0/tasks/next/claim", "agent-late", nil); code != 200 || field(body, "phase") != "Running" {
		t.Errorf("the agent's next task: %d %v", code, body)
	}
}
