//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/agent"
	"github.com/eigen-hq/cell-platform/internal/agent/agenttest"
)

// opsAgent is the agent with its account from the cell and a scripted
// model: the run is repeatable and costs nothing, everything else is the
// real cell.
func (tc *testCell) opsAgent(t *testing.T, script agenttest.Script) *agent.Agent {
	t.Helper()
	var s corev1.Secret
	if err := tc.kube.Get(context.Background(), client.ObjectKey{Namespace: "cell-system", Name: "cell-agent"}, &s); err != nil {
		t.Fatalf("the operator set up no agent account: %v", err)
	}
	model := agenttest.Serve(script)
	t.Cleanup(func() {
		model.Close()
		for _, err := range model.Errors() {
			t.Errorf("model: %v", err)
		}
	})
	m := anthropic.NewClient(option.WithAPIKey("scripted"), option.WithBaseURL(model.URL), option.WithMaxRetries(0))
	return &agent.Agent{
		API: &agent.API{URL: "https://" + tc.cfg.Host("api"), TokenURL: "https://" + tc.cfg.Host("auth") + "/application/o/token/",
			ClientID: "cell-api", Username: string(s.Data["username"]), Password: string(s.Data["password"]), HTTP: tc.akHTTP},
		Model: &m, ModelID: "scripted", Log: slog.Default(), Poll: time.Second,
	}
}

// pauseAgent stops the cell's own agent, which may have a real model key,
// for the length of the test.
func (tc *testCell) pauseAgent(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	scale := func(n int32) error {
		var d appsv1.Deployment
		if err := tc.kube.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-agent"}, &d); err != nil {
			return err
		}
		d.Spec.Replicas = ptr.To(n)
		return tc.kube.Update(ctx, &d)
	}
	running := func() error {
		var d appsv1.Deployment
		if err := tc.kube.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-agent"}, &d); err != nil {
			return err
		}
		return check(d.Status.AvailableReplicas == 1 && d.Status.UpdatedReplicas == 1, "the cell's agent does not run: %+v", d.Status)
	}
	if err := running(); err != nil {
		t.Fatal(err)
	}
	if err := scale(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := scale(1); err != nil {
			t.Errorf("restart the cell's agent: %v", err)
			return
		}
		waitFor(t, 2*time.Minute, running)
	})
	waitFor(t, 2*time.Minute, func() error {
		var pods corev1.PodList
		if err := tc.kube.List(ctx, &pods, client.InNamespace("cell-system"), client.MatchingLabels{"app.kubernetes.io/name": "cell-agent"}); err != nil {
			return err
		}
		if len(pods.Items) > 0 {
			return fmt.Errorf("%d agent pods left", len(pods.Items))
		}
		return nil
	})
}

// removed makes sure an app is gone with its data.
func (a *apiClient) removed(t *testing.T, name string) {
	t.Helper()
	if code, _ := a.call(t, http.MethodGet, "/v0/apps/"+name, nil); code == http.StatusNotFound {
		return
	}
	change := a.propose(t, a, http.MethodDelete, "/v0/apps/"+name+"?deleteData=true&reason=e2e", nil)
	a.waitChange(t, change, 10*time.Minute, "Succeeded")
}

func calls(task any) []string {
	var out []string
	for _, s := range field(task, "steps").([]any) {
		out = append(out, field(s, "call").(string))
	}
	return out
}

func check(cond bool, format string, args ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf(format, args...)
}

// Scenario A: a wrong configuration value makes an app
// unhealthy. The operator asks the agent, the agent finds the cause in the
// logs through the API and proposes the repair, an admin approves, the app
// is healthy again. Every step of the agent is in its task.
func TestAgentRepairsApp(t *testing.T) {
	tc := newTestCell(t)
	ctx := context.Background()
	admin := tc.adminAPI(t)
	tc.pauseAgent(t)
	admin.removed(t, "ailing")
	t.Cleanup(func() { admin.removed(t, "ailing") })
	install := admin.propose(t, admin, http.MethodPost, "/v0/apps", map[string]any{"catalog": "whoami", "name": "ailing", "subdomain": "ailing"})
	admin.waitChange(t, install, 5*time.Minute, "Succeeded")

	// The world: someone sets a wrong value past the platform.
	var app cellv1.App
	if err := tc.kube.Get(ctx, client.ObjectKey{Name: "ailing"}, &app); err != nil {
		t.Fatal(err)
	}
	app.Spec.Source.Image.Env = []corev1.EnvVar{{Name: "WHOAMI_PORT_NUMBER", Value: "8080"}}
	app.Spec.Health.TimeoutSeconds = 60
	injected := time.Now().Truncate(time.Second)
	if err := tc.kube.Update(ctx, &app); err != nil {
		t.Fatal(err)
	}
	t.Log("app ailing (whoami) installed, then WHOAMI_PORT_NUMBER=8080 set past the platform")

	var task string
	waitFor(t, 5*time.Minute, func() error {
		tasks := admin.do(t, http.MethodGet, "/v0/tasks?app=ailing&phase=Pending", nil, http.StatusOK).([]any)
		// Newest first; an earlier run may have left tasks for an earlier ailing.
		if len(tasks) == 0 {
			return fmt.Errorf("no diagnose task for ailing yet")
		}
		if created, err := time.Parse(time.RFC3339, field(tasks[0], "createdAt").(string)); err != nil || created.Before(injected) {
			return fmt.Errorf("no diagnose task for this ailing yet (newest %v, %v)", tasks[0], err)
		}
		task = field(tasks[0], "name").(string)
		return check(field(tasks[0], "requester", "kind") == "Operator", "task %v", tasks[0])
	})

	var change string
	a := tc.opsAgent(t, func(c agenttest.Conversation) (agenttest.Turn, error) {
		switch c.Turn {
		case 0:
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "get_app", Input: map[string]any{"app": "ailing"}}, {Tool: "get_pods", Input: map[string]any{"app": "ailing"}}}}, nil
		case 1:
			if err := check(len(c.Results) == 2 && strings.Contains(c.Results[0], "WHOAMI_PORT_NUMBER") && strings.Contains(c.Results[1], "ailing-"), "app and pods %q", c.Results); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "get_logs", Input: map[string]any{"app": "ailing"}}}}, nil
		case 2:
			if err := check(len(c.Results) == 1 && strings.Contains(c.Results[0], "Starting up on port 8080"), "logs %q", c.Results); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "propose_update", Input: map[string]any{"app": "ailing",
				"patch":  map[string]any{"source": map[string]any{"image": map[string]any{"env": nil}}},
				"reason": "WHOAMI_PORT_NUMBER=8080 moves whoami off port 80, where the platform routes and checks it (log: Starting up on port 8080)"}}}}, nil
		case 3:
			var cr struct{ Name string }
			if len(c.Results) != 1 || json.Unmarshal([]byte(c.Results[0]), &cr) != nil || cr.Name == "" {
				return agenttest.Turn{}, fmt.Errorf("proposal %q", c.Results)
			}
			change = cr.Name
			return agenttest.Turn{Text: "Cause: WHOAMI_PORT_NUMBER=8080. Proposed change " + cr.Name + "."}, nil
		}
		return agenttest.Turn{}, fmt.Errorf("turn %d", c.Turn)
	})
	t.Logf("the operator handed ailing to the agent: task %s", task)
	if err := a.Take(ctx, task); err != nil {
		t.Fatal(err)
	}
	done := admin.do(t, http.MethodGet, "/v0/tasks/"+task, nil, http.StatusOK)
	t.Logf("agent (scripted model) read %v and answered: %v", calls(done), field(done, "result"))
	got := calls(done)
	slices.Sort(got[:min(2, len(got))])
	if field(done, "phase") != "Succeeded" || field(done, "assignee") != "cell-agent" ||
		!slices.Equal(got, []string{"GET /v0/apps/ailing", "GET /v0/apps/ailing/pods", "GET /v0/apps/ailing/logs", "PATCH /v0/apps/ailing"}) ||
		!slices.Equal(field(done, "changes").([]any), []any{change}) || !strings.Contains(field(done, "result").(string), change) {
		t.Fatalf("task %v", done)
	}
	cr := admin.do(t, http.MethodGet, "/v0/changes/"+change, nil, http.StatusOK)
	if field(cr, "requester", "kind") != "Agent" || field(cr, "task") != task || field(cr, "risk") != "high" {
		t.Errorf("change %v", cr)
	}
	admin.do(t, http.MethodPost, "/v0/changes/"+change+"/approve", nil, http.StatusOK)
	admin.waitChange(t, change, 5*time.Minute, "Succeeded")
	waitFor(t, time.Minute, func() error {
		app := admin.do(t, http.MethodGet, "/v0/apps/ailing", nil, http.StatusOK)
		return check(field(app, "phase") == "Ready", "ailing is %v: %v", field(app, "phase"), field(app, "conditions"))
	})
	t.Logf("admin approved %s, ailing is healthy again", change)
}

// Scenario B: the agent packages an app that is not in the
// catalog from its Compose file; the operator installs the manifest without
// rework. The manifest is the scripted model's; what is real is that the
// API checks it and the cell runs it.
func TestAgentPackagesApp(t *testing.T) {
	tc := newTestCell(t)
	ctx := context.Background()
	admin := tc.adminAPI(t)
	tc.pauseAgent(t)
	admin.removed(t, "memos")
	t.Cleanup(func() { admin.removed(t, "memos") })
	compose := `services:
  memos:
    image: neosmemo/memos:26.11.1
    container_name: memos
    ports:
      - 5230:5230
    volumes:
      - ./memos:/var/opt/memos
    restart: unless-stopped
`
	task := field(admin.do(t, http.MethodPost, "/v0/tasks", map[string]any{"kind": "package", "app": "memos", "input": compose}, http.StatusCreated), "name").(string)
	manifest := map[string]any{"title": "Memos", "version": "26.11.1",
		"source": map[string]any{"image": map[string]any{"repository": "neosmemo/memos"}},
		"needs":  map[string]any{"volumes": []any{map[string]any{"name": "data", "size": "1Gi", "mountPath": "/var/opt/memos"}}},
		"access": map[string]any{"subdomain": "memos", "port": 5230, "auth": map[string]any{"mode": "proxy"}},
		"health": map[string]any{"path": "/", "timeoutSeconds": 300}}
	var change string
	a := tc.opsAgent(t, func(c agenttest.Conversation) (agenttest.Turn, error) {
		switch c.Turn {
		case 0:
			if err := check(strings.Contains(c.Task, "neosmemo/memos:26.11.1"), "brief %q", c.Task); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "get_catalog_entry", Input: map[string]any{"name": "whoami"}}}}, nil
		case 1:
			// A first try with a subdomain of the cell's: the cluster's own
			// rules refuse it before anyone approves.
			wrong := map[string]any{}
			for k, v := range manifest {
				wrong[k] = v
			}
			wrong["access"] = map[string]any{"subdomain": "api", "port": 5230, "auth": map[string]any{"mode": "proxy"}}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "propose_install", Input: map[string]any{"app": "memos", "manifest": wrong, "reason": "e2e"}}}}, nil
		case 2:
			if err := check(len(c.Results) == 1 && strings.HasPrefix(c.Results[0], "ERROR") && strings.Contains(c.Results[0], "belong to the cell"), "refusal %q", c.Results); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "propose_install", Input: map[string]any{"app": "memos", "manifest": manifest,
				"reason": "Memos from its Compose file: one container, data in /var/opt/memos, behind the cell's login"}}}}, nil
		case 3:
			var cr struct{ Name string }
			if len(c.Results) != 1 || json.Unmarshal([]byte(c.Results[0]), &cr) != nil || cr.Name == "" {
				return agenttest.Turn{}, fmt.Errorf("proposal %q", c.Results)
			}
			change = cr.Name
			return agenttest.Turn{Text: "Packaged memos; proposed change " + cr.Name + "."}, nil
		}
		return agenttest.Turn{}, fmt.Errorf("turn %d", c.Turn)
	})
	if err := a.Take(ctx, task); err != nil {
		t.Fatal(err)
	}
	done := admin.do(t, http.MethodGet, "/v0/tasks/"+task, nil, http.StatusOK)
	if field(done, "phase") != "Succeeded" || !slices.Equal(calls(done), []string{"GET /v0/catalog/whoami", "POST /v0/apps", "POST /v0/apps"}) {
		t.Fatalf("task %v", done)
	}
	cr := admin.do(t, http.MethodGet, "/v0/changes/"+change, nil, http.StatusOK)
	if field(cr, "action") != "install" || field(cr, "risk") != "high" || field(cr, "task") != task {
		t.Errorf("change %v", cr)
	}
	admin.do(t, http.MethodPost, "/v0/changes/"+change+"/approve", nil, http.StatusOK)
	admin.waitChange(t, change, 10*time.Minute, "Succeeded")
	t.Logf("agent packaged memos from its Compose file (first try refused by the dry run); admin approved %s, memos is installed", change)
}

// The API's account checks apps with dry runs but cannot create them: the
// operator installs apps, after approval (E-30).
func TestAPICannotCreateApps(t *testing.T) {
	tc := newTestCell(t)
	ctx := context.Background()
	cfg, err := ctrl.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Impersonate.UserName = "system:serviceaccount:cell-system:cell-api"
	api, err := client.New(cfg, client.Options{Scheme: tc.kube.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	app := &cellv1.App{}
	app.Name = "sneaky"
	app.Spec = cellv1.AppSpec{Version: "1", Source: cellv1.Source{Image: &cellv1.ImageSource{Repository: "x"}},
		Access: cellv1.Access{Subdomain: "sneaky", Port: 80, Auth: cellv1.Auth{Mode: "none"}}}
	if err := api.Create(ctx, app.DeepCopy(), client.DryRunAll); err != nil {
		t.Errorf("dry run as the API: %v", err)
	}
	if err := api.Create(ctx, app.DeepCopy()); err == nil || !strings.Contains(err.Error(), "only checks apps") {
		t.Errorf("the API's account created an app: %v", err)
		_ = tc.kube.Delete(ctx, app)
	}
}
