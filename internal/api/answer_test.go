package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// An agent ends a task with its answer in parts (E-100). Each of its calls
// in the task is answered with the step it was recorded as, refused ones
// too, so the answer can name the steps that show a finding. The API
// checks the answer against the task, keeps it and renders it as the
// result; check mode only checks.
func TestFinishAnswer(t *testing.T) {
	foreign := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}, Spec: cellv1.ChangeRequestSpec{App: "notes", Task: "notes-diagnose-2"}}
	old := pendingTask("notes-diagnose-3")
	old.Status = cellv1.TaskStatus{Phase: cellv1.RunRunning, Assignee: opsAccount, Answer: &cellv1.TaskAnswer{Summary: "old"}}
	h := newHarness(t, installed("notes", "oidc"), installed("plain", "none"), pendingTask("notes-diagnose-1"), pendingTask("notes-diagnose-2"), foreign, old)
	h.agentTask = "notes-diagnose-1"
	if code, body := h.do("POST", "/v0/tasks/notes-diagnose-1/claim", "agent", nil); code != 200 {
		t.Fatalf("claim: %d %v", code, body)
	}
	for i, path := range []string{"/v0/apps/notes", "/v0/apps/plain"} {
		req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer agent")
		req.Header.Set(TaskHeader, "notes-diagnose-1")
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get(StepHeader); got != []string{"1", "2"}[i] {
			t.Errorf("GET %s (%d): step %q, want %d", path, resp.StatusCode, got, i+1)
		}
	}
	code, body := h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"version": "1.0"}, "reason": "1.1 does not exist"})
	if code != 202 {
		t.Fatalf("proposal: %d %v", code, body)
	}
	change := field(body, "name").(string)

	finish := func(body map[string]any) (int, any) {
		return h.do("POST", "/v0/tasks/notes-diagnose-1/finish", "agent", body)
	}
	for _, c := range []struct{ answer, want string }{
		{`{"summary":" "}`, "needs a summary"},
		{`{"summary":"` + strings.Repeat("ä", 2001) + `"}`, "2001 characters"},
		{`{"summary":"x","extra":1}`, "unknown field"},
		{`{"summary":"x","findings":[{"claim":"c","status":"maybe"}]}`, "status must be"},
		{`{"summary":"x","findings":[{"claim":"c","status":"confirmed","steps":[0]}]}`, "names step 0"},
		{`{"summary":"x","findings":[{"claim":"c","status":"confirmed","steps":[4]}]}`, "names step 4; the task has steps 1 to 3"},
		{`{"summary":"x","options":[{"title":"a","recommended":true},{"title":"b","recommended":true}]}`, "at most one option is recommended"},
		{`{"summary":"x","options":[{"title":"a","change":"CH"},{"title":"b","change":"CH"}]}`, "at most one option names a change"},
		{`{"summary":"x","options":[{"title":"a","change":"nothing"}]}`, "this task did not propose"},
		{`{"summary":"x","options":[{"title":"a","change":"foreign"}]}`, "this task did not propose"},
	} {
		code, body := finish(map[string]any{"answer": json.RawMessage(strings.ReplaceAll(c.answer, "CH", change)), "check": true})
		if msg, _ := field(body, "error").(string); code != 400 || !strings.Contains(msg, c.want) {
			t.Errorf("answer %.60s: %d %v, want 400 with %q", c.answer, code, body, c.want)
		}
	}
	answer := map[string]any{"summary": "Version 1.1 does not exist.", "understood": "Why notes is down.",
		"findings": []any{map[string]any{"claim": "The image tag 1.1 is missing.", "status": "confirmed", "steps": []int{1, 3}}},
		"options":  []any{map[string]any{"title": "Go back to 1.0", "why": "It ran.", "change": change, "recommended": true}, map[string]any{"title": "Wait for 1.1"}}}
	for _, body := range []map[string]any{{"answer": answer, "failed": true}, {"answer": answer, "waiting": true}, {"answer": answer, "result": "x"}, {"check": true}} {
		if code, _ := finish(body); code != 400 {
			t.Errorf("finish %v: %d, want 400", body, code)
		}
	}
	if code, body := finish(map[string]any{"answer": answer, "check": true}); code != 200 || field(body, "phase") != "Running" || field(body, "answer") != nil {
		t.Errorf("check: %d %v", code, body)
	}
	if n := len(h.steps("notes-diagnose-1")); n != 3 {
		t.Errorf("the task has %d steps after the check, want 3", n)
	}
	code, body = finish(map[string]any{"answer": answer})
	want := "Version 1.1 does not exist.\n\nUnderstood: Why notes is down.\n\nFound:\n- [confirmed] The image tag 1.1 is missing. (steps 1, 3)\n\n" +
		"Options:\n1. Go back to 1.0 (recommended): It ran. Change: " + change + "\n2. Wait for 1.1"
	if code != 200 || field(body, "phase") != "Succeeded" || field(body, "result") != want || field(body, "answer", "options").([]any)[0].(map[string]any)["change"] != change {
		t.Errorf("finish with the answer: %d %v\nresult %q", code, body, field(body, "result"))
	}

	// A later answer as text drops the earlier one in parts.
	h.agentTask = old.Name
	if code, body := h.do("POST", "/v0/tasks/notes-diagnose-3/finish", "agent", map[string]any{"result": "new"}); code != 200 || field(body, "answer") != nil {
		t.Errorf("finish as text: %d %v", code, body)
	}
	var got cellv1.Task
	if err := h.kube.Get(context.Background(), client.ObjectKey{Name: old.Name}, &got); err != nil || got.Status.Answer != nil {
		t.Errorf("stored answer %v, %v", got.Status.Answer, err)
	}
}

func TestCut(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{{"abc", 3, "abc"}, {"abcd", 3, "abc"}, {"aä", 2, "a"}, {"aäb", 3, "aä"}} {
		if got := cut(c.in, c.n); got != c.want {
			t.Errorf("cut(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
