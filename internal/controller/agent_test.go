package controller

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// An app that needs attention gets one diagnose task for the ops agent
// each time it gets there, not one per reconcile.
func TestAskAgent(t *testing.T) {
	ctx := context.Background()
	since := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	now := since.Add(10 * time.Minute) // past the default health timeout
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "ailing"}, Status: cellv1.AppStatus{Phase: cellv1.PhaseNeedsAttention,
		Conditions: []metav1.Condition{
			{Type: condReady, Status: metav1.ConditionFalse, Reason: "Unhealthy", Message: "health check: connection refused", LastTransitionTime: metav1.NewTime(since)},
			{Type: "WorkloadReady", Status: metav1.ConditionFalse, Reason: "Progressing", Message: "0/1 ready", LastTransitionTime: metav1.NewTime(since.Add(time.Minute))},
		}}}
	r := &AppReconciler{Client: fakeClient(k8s.Scheme()), Now: func() time.Time { return now }}
	ask := func(act activity, want int) []cellv1.Task {
		t.Helper()
		if err := r.askAgent(ctx, app, act); err != nil {
			t.Fatal(err)
		}
		var list cellv1.TaskList
		if err := r.List(ctx, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != want {
			t.Fatalf("%d tasks, want %d", len(list.Items), want)
		}
		return list.Items
	}
	ask(activity{}, 1)
	task := ask(activity{}, 1)[0]
	if !strings.HasPrefix(task.Name, "ailing-diagnose-") || task.Spec.Kind != "diagnose" || task.Spec.Requester.Kind != "Operator" ||
		!strings.Contains(task.Spec.Input, "health checks fail since 2026-11-19T12:00:00Z") ||
		!strings.Contains(task.Spec.Input, "connection refused") || !strings.Contains(task.Spec.Input, "0/1 ready") {
		t.Errorf("task %s %+v", task.Name, task.Spec)
	}
	// Changing health check messages and flapping workload conditions are
	// the same cause.
	app.Status.Conditions[0].Message = "health check: timeout"
	app.Status.Conditions[1].LastTransitionTime = metav1.NewTime(since.Add(2 * time.Minute))
	ask(activity{}, 1)
	// Each failed change is a cause of its own, also while the app is
	// unhealthy; recovering health does not make it a new one.
	ask(activity{failedChange: "change a: RolledBack"}, 2)
	app.Status.Conditions[0].Status = metav1.ConditionTrue
	ask(activity{failedChange: "change a: RolledBack"}, 2)
	ask(activity{failedChange: "change b: RolledBack"}, 3)
	// So is each failed restore that holds the app.
	ask(activity{reason: "restore r1 failed while replacing data"}, 4)
	ask(activity{reason: "restore r2 failed while replacing data"}, 5)
	ask(activity{reason: "restore r2 failed while replacing data"}, 5)
	// A new stretch of failed health checks counts once it is too long.
	app.Status.Conditions[0].Status = metav1.ConditionFalse
	app.Status.Conditions[0].LastTransitionTime = metav1.NewTime(since.Add(time.Hour))
	now = since.Add(time.Hour + time.Minute)
	ask(activity{failedChange: "change b: RolledBack"}, 5)
	now = since.Add(time.Hour + 10*time.Minute)
	ask(activity{failedChange: "change b: RolledBack"}, 6)
	app.Status.Phase = cellv1.PhaseReady
	ask(activity{}, 6)
}

type fakeAgentIdentity struct {
	accounts  map[string][]string
	passwords int
}

func (f *fakeAgentIdentity) EnsureServiceAccount(_ context.Context, username string, groups ...string) error {
	f.accounts[username] = groups
	return nil
}

func (f *fakeAgentIdentity) CreateAppPassword(context.Context, string) (string, error) {
	f.passwords++
	return "pw-" + strconv.Itoa(f.passwords), nil
}

// The agent's account is set up once: its credentials are not replaced on
// every operator start.
func TestEnsureAgentAccount(t *testing.T) {
	ctx := context.Background()
	c := fakeClient(k8s.Scheme())
	id := &fakeAgentIdentity{accounts: map[string][]string{}}
	for range 2 {
		if err := EnsureAgentAccount(ctx, c, id); err != nil {
			t.Fatal(err)
		}
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-agent"}, &s); err != nil {
		t.Fatal(err)
	}
	if string(s.Data["username"]) != "cell-agent" || string(s.Data["password"]) != "pw-1" || id.passwords != 1 {
		t.Errorf("secret %v, %d passwords", s.Data, id.passwords)
	}
	if g := id.accounts["cell-agent"]; len(g) != 1 || g[0] != "cell-agents" {
		t.Errorf("account groups %v", g)
	}
	// A secret without a password (made by hand) gets one.
	s.Data = map[string][]byte{"username": []byte("cell-agent")}
	if err := c.Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentAccount(ctx, c, id); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&s), &s); err != nil || string(s.Data["password"]) != "pw-2" {
		t.Errorf("secret %v: %v", s.Data, err)
	}
}
