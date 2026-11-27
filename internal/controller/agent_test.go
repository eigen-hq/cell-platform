package controller

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
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
	mu        sync.Mutex
	accounts  map[string][]string
	passwords int
	valid     map[string][]string // username -> app passwords that log in
}

func (f *fakeAgentIdentity) EnsureServiceAccount(_ context.Context, username string, groups ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[username] = groups
	return nil
}

func (f *fakeAgentIdentity) CreateAppPassword(_ context.Context, username string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passwords++
	pw := "pw-" + strconv.Itoa(f.passwords)
	if f.valid == nil {
		f.valid = map[string][]string{}
	}
	f.valid[username] = append(f.valid[username], pw)
	return pw, nil
}

func (f *fakeAgentIdentity) RevokeAppPasswords(_ context.Context, username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.valid, username)
	return nil
}

func (f *fakeAgentIdentity) loginsOf(username string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.valid[username])
}

func (f *fakeAgentIdentity) groups(username string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts[username]
}

// Each Agent gets its account, credentials made once and a Deployment that
// runs it with them, under the operator's real rights (E-97).
func TestAgent(t *testing.T) {
	ctx := context.Background()
	a := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "probe"},
		Spec: cellv1.AgentSpec{Kinds: []string{"diagnose"}, Apps: []string{"*"}, Tools: []string{"*"}}}
	if err := k8s.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.IgnoreNotFound(k8s.Delete(ctx, a)); err != nil {
			t.Error(err)
		}
	})
	var s corev1.Secret
	var d appsv1.Deployment
	key := client.ObjectKey{Namespace: "cell-system", Name: "cell-agent-probe"}
	eventually(t, func() error {
		if err := k8s.Get(ctx, key, &s); err != nil {
			return err
		}
		return k8s.Get(ctx, key, &d)
	})
	password := string(s.Data["password"])
	if string(s.Data["username"]) != "cell-agent-probe" || !strings.HasPrefix(password, "pw-") || len(s.Data["modelKey"]) != 64 || !metav1.IsControlledBy(&s, a) {
		t.Errorf("secret %v, owners %v", s.Data, s.OwnerReferences)
	}
	if g := agentIdent.groups("cell-agent-probe"); !slices.Equal(g, []string{"cell-agents"}) {
		t.Errorf("account groups %v", g)
	}
	c := d.Spec.Template.Spec.Containers[0]
	if c.Image != "agent:test" || *d.Spec.Replicas != 1 || !metav1.IsControlledBy(&d, a) || *d.Spec.Template.Spec.AutomountServiceAccountToken ||
		len(c.Env) != 3 || c.Env[1].ValueFrom.SecretKeyRef.Name != "cell-agent-probe" {
		t.Errorf("deployment %+v", d.Spec)
	}
	// Paused, it runs no pod and keeps its credentials.
	if err := k8s.Patch(ctx, a, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"paused":true}}`))); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if err := k8s.Get(ctx, key, &d); err != nil {
			return err
		}
		if *d.Spec.Replicas != 0 {
			return fmt.Errorf("replicas %d", *d.Spec.Replicas)
		}
		return nil
	})
	if err := k8s.Get(ctx, key, &s); err != nil || string(s.Data["password"]) != password {
		t.Errorf("password %q after pause: %v", s.Data["password"], err)
	}

	// New credentials, as for a later Agent of the same name, end the old.
	if err := k8s.Delete(ctx, &s); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		s = corev1.Secret{}
		if err := k8s.Get(ctx, key, &s); err != nil {
			return err
		}
		if string(s.Data["password"]) == password {
			return fmt.Errorf("still the old password")
		}
		return nil
	})
	if l := agentIdent.loginsOf("cell-agent-probe"); !slices.Equal(l, []string{string(s.Data["password"])}) {
		t.Errorf("passwords that log in %v, want only %q", l, s.Data["password"])
	}
	// and restart its pod with them.
	eventually(t, func() error {
		if err := k8s.Get(ctx, key, &d); err != nil {
			return err
		}
		if got := d.Spec.Template.Annotations["cell.internal/credentials"]; got != string(s.UID) {
			return fmt.Errorf("pod template for credentials %q, want %q", got, s.UID)
		}
		return nil
	})

	// A deleted Agent's passwords stop logging in before its name is free.
	if err := k8s.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(a), &cellv1.Agent{}); !apierrors.IsNotFound(err) {
			return fmt.Errorf("agent still there: %v", err)
		}
		return nil
	})
	if l := agentIdent.loginsOf("cell-agent-probe"); len(l) != 0 {
		t.Errorf("passwords of a deleted agent %v", l)
	}

	// A Secret of that name the Agent does not own stays as it is.
	stray := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-agent-stray"}}
	if err := k8s.Create(ctx, stray); err != nil {
		t.Fatal(err)
	}
	sa := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "stray"}, Spec: a.Spec}
	if err := k8s.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	r := &AgentReconciler{Client: k8s, Identity: agentIdent, Image: "agent:test"}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "stray"}}); err == nil || !strings.Contains(err.Error(), "is not agent stray's") {
		t.Errorf("foreign secret: %v", err)
	}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(stray), stray); err != nil || len(stray.Data) != 0 {
		t.Errorf("foreign secret %v: %v", stray.Data, err)
	}

	// Secret cell-agent-llm holds the model settings.
	if err := k8s.Create(ctx, &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "llm"}, Spec: a.Spec}); !apierrors.IsInvalid(err) {
		t.Errorf("agent llm: %v", err)
	}
}
