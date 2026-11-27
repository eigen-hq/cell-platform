package api

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// An agent's calls end at its Agent (E-97): it sees, claims and works in
// only the tasks it takes and calls only its tools; an account in the
// agents' group without an Agent may do nothing.
func TestAgentSpec(t *testing.T) {
	reader := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "reader", UID: "reader-1"},
		Spec: cellv1.AgentSpec{Kinds: []string{"diagnose"}, Apps: []string{"notes"}, Tools: []string{"getApp", "listApps", "installApp"}, Instructions: "Be brief."}}
	other := pendingTask("plain-diagnose-1")
	other.Spec.App = "plain"
	gone := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "gone", UID: "gone-1", Finalizers: []string{"cell.internal/agent"}, DeletionTimestamp: &metav1.Time{Time: time.Now()}},
		Spec: reader.Spec}
	// A later Agent of the name than the one token agent-again is of.
	again := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "again", UID: "again-2"}, Spec: reader.Spec}
	h := newHarness(t, installed("notes", "oidc"), installed("plain", "none"), reader, gone, again, pendingTask("notes-diagnose-1"), other)
	h.agentTask = ""
	// An Agent being deleted may do nothing: its passwords are on their way out.
	if code, body := h.do("GET", "/v0/whoami", "agent-gone", nil); code != 403 {
		t.Errorf("agent being deleted: %d %v", code, body)
	}
	// Nor may the account of an earlier Agent of the name: its tokens may
	// still be cached after the account went.
	if code, body := h.do("GET", "/v0/whoami", "agent-again", nil); code != 403 || !strings.Contains(field(body, "error").(string), "earlier agent again") {
		t.Errorf("account of an earlier agent: %d %v", code, body)
	}
	// Nor an account that is not named after an Agent and its UID.
	for _, account := range []string{"cell-agent-reader", "cell-agent-.reader-1", "reader.reader-1"} {
		if code, body := h.do("GET", "/v0/whoami", "account:"+account, nil); code != 403 || !strings.Contains(field(body, "error").(string), "no agent of the cell") {
			t.Errorf("account %s: %d %v", account, code, body)
		}
	}

	code, body := h.do("GET", "/v0/whoami", "agent-reader", nil)
	if code != 200 || field(body, "agent", "name") != "reader" || field(body, "agent", "instructions") != "Be brief." || field(body, "agent", "tools").([]any)[0] != "getApp" {
		t.Errorf("whoami of an agent: %d %v", code, body)
	}
	if _, body := h.do("GET", "/v0/whoami", "admin", nil); field(body, "agent") != nil {
		t.Errorf("whoami of a person: %v", body)
	}
	if code, body := h.do("GET", "/v0/whoami", "agent-ghost", nil); code != 403 || !strings.Contains(field(body, "error").(string), "there is no agent ghost") {
		t.Errorf("agent account without an Agent: %d %v", code, body)
	}

	// It sees only the tasks it takes.
	code, body = h.do("GET", "/v0/tasks", "agent-reader", nil)
	var names []string
	for _, v := range body.([]any) {
		names = append(names, field(v, "name").(string))
	}
	if slices.Sort(names); code != 200 || !slices.Equal(names, []string{agentWork, "notes-diagnose-1"}) {
		t.Errorf("tasks: %d %v", code, names)
	}
	if code, _ := h.do("GET", "/v0/tasks/plain-diagnose-1", "agent-reader", nil); code != 403 {
		t.Errorf("task of another app: %d", code)
	}
	if code, _ := h.do("POST", "/v0/tasks/plain-diagnose-1/claim", "agent-reader", nil); code != 403 {
		t.Errorf("claim a task of another app: %d", code)
	}
	if code, body := h.do("POST", "/v0/tasks/notes-diagnose-1/claim", "agent-reader", nil); code != 200 {
		t.Fatalf("claim: %d %v", code, body)
	}

	// In its task it calls only its tools.
	h.agentTask = "notes-diagnose-1"
	if code, body := h.do("GET", "/v0/apps/notes", "agent-reader", nil); code != 200 {
		t.Errorf("its tool: %d %v", code, body)
	}
	if code, body := h.do("GET", "/v0/apps/notes/logs", "agent-reader", nil); code != 403 || !strings.Contains(field(body, "error").(string), "is not a tool of agent reader") {
		t.Errorf("another tool: %d %v", code, body)
	}
	// Lists and installs stay in the task's app too.
	code, body = h.do("GET", "/v0/apps", "agent-reader", nil)
	if apps, _ := body.([]any); code != 200 || len(apps) != 1 || field(apps[0], "name") != "notes" {
		t.Errorf("apps in its task: %d %v", code, body)
	}
	if code, body := h.do("POST", "/v0/apps", "agent-reader", map[string]any{"name": "other", "manifest": map[string]any{}}); code != 403 {
		t.Errorf("install another app: %d %v", code, body)
	}

	// An admin narrows the Agent: its next call stops.
	ctx := context.Background()
	if err := h.kube.Get(ctx, client.ObjectKeyFromObject(reader), reader); err != nil {
		t.Fatal(err)
	}
	reader.Spec.Apps = []string{"plain"}
	if err := h.kube.Update(ctx, reader); err != nil {
		t.Fatal(err)
	}
	if code, body := h.do("GET", "/v0/apps/notes", "agent-reader", nil); code != 403 || !strings.Contains(field(body, "error").(string), "takes no diagnose task about app notes") {
		t.Errorf("after the Agent changed: %d %v", code, body)
	}
}
