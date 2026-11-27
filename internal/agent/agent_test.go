package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apidoc "github.com/eigen-hq/cell-platform/api"
	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/catalog"
	"github.com/eigen-hq/cell-platform/internal/agent/agenttest"
	cellapi "github.com/eigen-hq/cell-platform/internal/api"
)

// cell is the platform API over a fake cluster, with the agent's account.
type cell struct {
	kube  client.WithWatch
	agent *Agent
}

func newCell(t *testing.T, script agenttest.Script, objs ...client.Object) *cell {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = cellv1.AddToScheme(scheme)
	var pods []runtime.Object
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok {
			pods = append(pods, p)
		}
	}
	// The operator tells that a provider is set.
	objs = append(objs, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-models-status"}, Data: map[string]string{"models.json": `{"configured":true}`}})
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&cellv1.App{}, &cellv1.Task{}, &cellv1.ChangeRequest{}).Build()
	api := httptest.NewServer((&cellapi.Server{
		Kube: kube, Pods: k8sfake.NewSimpleClientset(pods...).CoreV1(), Catalog: catalog.FS, OpenAPI: apidoc.OpenAPI,
		Authenticate: func(_ context.Context, token string) (*cellapi.Caller, error) {
			if token != "agent-token" {
				return nil, errors.New("bad token")
			}
			return &cellapi.Caller{Username: "cell-agent", Role: cellapi.RoleAgent, Groups: []string{"cell-agents"}}, nil
		},
	}).Handler())
	t.Cleanup(api.Close)
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") != "client_credentials" || r.FormValue("client_id") != "cell-api" ||
			r.FormValue("username") != "cell-agent" || r.FormValue("password") != "app-password" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"agent-token","expires_in":300}`)
	}))
	t.Cleanup(identity.Close)
	model := agenttest.Serve(script)
	t.Cleanup(func() {
		model.Close()
		for _, err := range model.Errors() {
			t.Errorf("model: %v", err)
		}
	})
	return &cell{kube: kube, agent: &Agent{
		API:   &API{URL: api.URL, TokenURL: identity.URL, ClientID: "cell-api", Username: "cell-agent", Password: "app-password", HTTP: http.DefaultClient},
		Model: &Model{URL: model.URL, Key: "test", Name: "scripted", HTTP: http.DefaultClient},
		Log:   slog.New(slog.DiscardHandler), Poll: time.Second,
	}}
}

func (c *cell) task(t *testing.T, name string) (*cellv1.Task, []string) {
	t.Helper()
	var task cellv1.Task
	if err := c.kube.Get(context.Background(), client.ObjectKey{Name: name}, &task); err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, s := range task.Status.Steps {
		calls = append(calls, s.Call)
	}
	return &task, calls
}

func (c *cell) changes(t *testing.T) []cellv1.ChangeRequest {
	t.Helper()
	var list cellv1.ChangeRequestList
	if err := c.kube.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func want(cond bool, format string, args ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf(format, args...)
}

// Scenario A: the agent gathers evidence through the API, proposes the
// repair as a change linked to its task, and answers; the task holds every
// call it made.
func TestDiagnose(t *testing.T) {
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "board"},
		Spec: cellv1.AppSpec{Version: "1.0", Source: cellv1.Source{Image: &cellv1.ImageSource{Repository: "example/board",
			Env: []corev1.EnvVar{{Name: "PORT", Value: "8080"}}}},
			Access: cellv1.Access{Subdomain: "board", Port: 80, Auth: cellv1.Auth{Mode: "none"}}},
		Status: cellv1.AppStatus{Phase: cellv1.PhaseNeedsAttention, Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse,
			Reason: "Unhealthy", Message: "health check: connection refused", LastTransitionTime: metav1.Now()}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "board-1", Namespace: "app-board"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "board"}}}}
	task := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "board-diagnose-1"},
		Spec: cellv1.TaskSpec{Kind: "diagnose", App: "board", Input: "Ready=False: connection refused", Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}}}
	var change string
	c := newCell(t, func(conv agenttest.Conversation) (agenttest.Turn, error) {
		switch conv.Turn {
		case 0:
			if err := want(strings.Contains(conv.Task, "board-diagnose-1") && strings.Contains(conv.Task, "connection refused"), "brief %q", conv.Task); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "get_app", Input: map[string]any{"app": "board"}}, {Tool: "get_pods", Input: map[string]any{"app": "board"}}}}, nil
		case 1:
			if err := want(len(conv.Results) == 2 && strings.Contains(conv.Results[0], "NeedsAttention") && strings.Contains(conv.Results[1], "board-1"), "results %q", conv.Results); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "get_logs", Input: map[string]any{"app": "board", "tail": 50}}}}, nil
		case 2:
			if err := want(slices.Equal(conv.Results, []string{"fake logs"}), "logs %q", conv.Results); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "propose_update", Input: map[string]any{"app": "board",
				"patch": map[string]any{"source": map[string]any{"image": map[string]any{"env": nil}}}, "reason": "PORT=8080 moves the app off its port 80"}}}}, nil
		case 3:
			var cr struct {
				Name string `json:"name"`
				Task string `json:"task"`
			}
			if len(conv.Results) != 1 || json.Unmarshal([]byte(conv.Results[0]), &cr) != nil || cr.Task != "board-diagnose-1" {
				return agenttest.Turn{}, fmt.Errorf("proposal result %q", conv.Results)
			}
			change = cr.Name
			return agenttest.Turn{Text: "Cause: PORT=8080. Proposed change " + cr.Name + "."}, nil
		}
		return agenttest.Turn{}, fmt.Errorf("turn %d", conv.Turn)
	}, app, pod, task)

	if err := c.agent.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, calls := c.task(t, "board-diagnose-1")
	// The two calls of the first turn run side by side.
	if len(calls) >= 2 {
		slices.Sort(calls[:2])
	}
	if got.Status.Phase != cellv1.RunSucceeded || got.Status.Assignee != "cell-agent" || !strings.Contains(got.Status.Result, "Proposed change "+change) {
		t.Errorf("task %+v", got.Status)
	}
	if want := []string{"GET /v0/apps/board", "GET /v0/apps/board/pods", "GET /v0/apps/board/logs?tail=50", "PATCH /v0/apps/board"}; !slices.Equal(calls, want) {
		t.Errorf("steps %q, want %q", calls, want)
	}
	crs := c.changes(t)
	if len(crs) != 1 || crs[0].Name != change || crs[0].Spec.Task != "board-diagnose-1" || crs[0].Spec.Requester.Kind != "Agent" ||
		string(crs[0].Spec.Patch.Raw) != `{"source":{"image":{"env":null}}}` {
		t.Errorf("changes %+v", crs)
	}
}

// Scenario B: the agent packages a Compose file; the API refuses its
// first manifest and the agent fixes it.
func TestPackage(t *testing.T) {
	compose := "services:\n  memo:\n    image: example/memo:0.25.3\n    ports: [\"5230:5230\"]\n    volumes: [\"./data:/var/opt/memo\"]\n"
	task := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "memo-package-1"},
		Spec: cellv1.TaskSpec{Kind: "package", App: "memo", Input: compose, Requester: cellv1.Requester{Kind: "Human", Name: "akadmin"}}}
	manifest := map[string]any{"version": "0.25.3", "source": map[string]any{"image": map[string]any{"repository": "example/memo"}},
		"needs":  map[string]any{"volumes": []any{map[string]any{"name": "data", "size": "1Gi", "mountPath": "/var/opt/memo"}}},
		"access": map[string]any{"subdomain": "memo", "port": 5230, "auth": map[string]any{"mode": "proxy"}}}
	c := newCell(t, func(conv agenttest.Conversation) (agenttest.Turn, error) {
		switch conv.Turn {
		case 0:
			if err := want(strings.Contains(conv.Task, "example/memo:0.25.3"), "brief %q", conv.Task); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "get_catalog_entry", Input: map[string]any{"name": "whoami"}}}}, nil
		case 1:
			if err := want(len(conv.Results) == 1 && strings.Contains(conv.Results[0], "traefik/whoami"), "catalog %q", conv.Results); err != nil {
				return agenttest.Turn{}, err
			}
			wrong := map[string]any{"ports": []any{"5230:5230"}}
			for k, v := range manifest {
				wrong[k] = v
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "propose_install", Input: map[string]any{"app": "memo", "manifest": wrong, "reason": "from Compose"}}}}, nil
		case 2:
			if err := want(len(conv.Results) == 1 && strings.HasPrefix(conv.Results[0], "ERROR") && strings.Contains(conv.Results[0], "ports"), "refusal %q", conv.Results); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Calls: []agenttest.Call{{Tool: "propose_install", Input: map[string]any{"app": "memo", "manifest": manifest, "reason": "from Compose"}}}}, nil
		case 3:
			return agenttest.Turn{Text: "Packaged memo."}, nil
		}
		return agenttest.Turn{}, fmt.Errorf("turn %d", conv.Turn)
	}, task)

	if err := c.agent.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, calls := c.task(t, "memo-package-1")
	if got.Status.Phase != cellv1.RunSucceeded || got.Status.Result != "Packaged memo." {
		t.Errorf("task %+v", got.Status)
	}
	if want := []string{"GET /v0/catalog/whoami", "POST /v0/apps", "POST /v0/apps"}; !slices.Equal(calls, want) {
		t.Errorf("steps %q, want %q", calls, want)
	}
	crs := c.changes(t)
	var spec cellv1.AppSpec
	if len(crs) != 1 || crs[0].Spec.Action != "install" || crs[0].Spec.Risk != "high" || crs[0].Spec.Task != "memo-package-1" ||
		json.Unmarshal(crs[0].Spec.Patch.Raw, &spec) != nil || spec.Access.Port != 5230 || spec.Needs.Volumes[0].MountPath != "/var/opt/memo" {
		t.Errorf("changes %+v", crs)
	}
}

// A turn cut at the token limit or by a filter runs none of its calls: the
// model hears so and goes on. A second cut in a row ends the task.
func TestCutTurn(t *testing.T) {
	for _, finish := range []string{"length", "content_filter"} {
		for _, again := range []bool{false, true} {
			task := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "board-diagnose-1"},
				Spec: cellv1.TaskSpec{Kind: "diagnose", App: "board", Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}}}
			c := newCell(t, func(conv agenttest.Conversation) (agenttest.Turn, error) {
				if len(conv.Notes) == 0 || again {
					return agenttest.Turn{Finish: finish, Calls: []agenttest.Call{{Tool: "propose_update",
						Input: map[string]any{"app": "board", "patch": map[string]any{"version": "2.0"}, "reason": "cut"}}}}, nil
				}
				if err := want(len(conv.Notes) == 1 && strings.Contains(conv.Notes[0], "("+finish+")"), "notes %q", conv.Notes); err != nil {
					return agenttest.Turn{}, err
				}
				return agenttest.Turn{Text: "Nothing to change."}, nil
			}, task)
			if err := c.agent.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
			phase, result := cellv1.RunSucceeded, "Nothing to change."
			if again {
				phase, result = cellv1.RunFailed, "("+finish+", twice in a row)"
			}
			got, calls := c.task(t, "board-diagnose-1")
			if got.Status.Phase != phase || !strings.Contains(got.Status.Result, result) || len(calls) != 0 || len(c.changes(t)) != 0 {
				t.Errorf("%s, again %v: task %+v, steps %q", finish, again, got.Status, calls)
			}
		}
	}
}

// A task the model cannot finish fails with the reason, and a restarted
// agent ends the tasks it left running.
func TestFailures(t *testing.T) {
	stuck := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "board-diagnose-1"},
		Spec:   cellv1.TaskSpec{Kind: "diagnose", App: "board", Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}},
		Status: cellv1.TaskStatus{Phase: cellv1.RunRunning, Assignee: "cell-agent"}}
	others := stuck.DeepCopy()
	others.Name, others.Status.Assignee = "board-diagnose-2", "other-agent"
	pending := stuck.DeepCopy()
	pending.Name, pending.Status = "board-diagnose-3", cellv1.TaskStatus{}
	c := newCell(t, func(agenttest.Conversation) (agenttest.Turn, error) {
		return agenttest.Turn{}, nil // empty answer: stops without text
	}, stuck, others, pending)
	if err := c.agent.abandon(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.task(t, "board-diagnose-1"); got.Status.Phase != cellv1.RunFailed || !strings.Contains(got.Status.Result, "interrupted") {
		t.Errorf("own running task %+v", got.Status)
	}
	if got, _ := c.task(t, "board-diagnose-2"); got.Status.Phase != cellv1.RunRunning {
		t.Errorf("another agent's task %+v", got.Status)
	}
	// The model's server fails: the task fails with the error, the agent
	// goes on.
	// The API fails the first attempt to finish: the agent tries again
	// instead of leaving the task running.
	c.agent.Model = &Model{URL: "http://127.0.0.1:1", HTTP: http.DefaultClient}
	target, err := url.Parse(c.agent.API.URL)
	if err != nil {
		t.Fatal(err)
	}
	var finishes atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/finish") && finishes.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	c.agent.API.URL = proxy.URL
	if err := c.agent.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.task(t, "board-diagnose-3"); got.Status.Phase != cellv1.RunFailed || !strings.HasPrefix(got.Status.Result, "model: ") || finishes.Load() != 2 {
		t.Errorf("task without a model %+v after %d finish requests", got.Status, finishes.Load())
	}
}

// Many pending tasks with large inputs: the agent reads the whole list
// and works on every task.
func TestLargeBacklog(t *testing.T) {
	var tasks []client.Object
	input := strings.Repeat("x", 64000)
	for i := range 20 {
		tasks = append(tasks, &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app%d-package-1", i)},
			Spec: cellv1.TaskSpec{Kind: "package", App: fmt.Sprintf("app%d", i), Input: input, Requester: cellv1.Requester{Kind: "Human", Name: "admin"}}})
	}
	c := newCell(t, nil, tasks...)
	c.agent.Model = &Model{URL: "http://127.0.0.1:1", HTTP: http.DefaultClient}
	if err := c.agent.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if got, _ := c.task(t, fmt.Sprintf("app%d-package-1", i)); got.Status.Phase != cellv1.RunFailed {
			t.Errorf("task %d %+v", i, got.Status.Phase)
		}
	}
}

// The API claims a task for the agent, but the answer gets lost: the
// agent ends the task on its next poll instead of leaving it running.
func TestLostClaim(t *testing.T) {
	pending := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "board-diagnose-1"},
		Spec: cellv1.TaskSpec{Kind: "diagnose", App: "board", Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}}}
	c := newCell(t, func(agenttest.Conversation) (agenttest.Turn, error) {
		return agenttest.Turn{}, errors.New("the model must not run")
	}, pending)
	target, err := url.Parse(c.agent.API.URL)
	if err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") && claims.Add(1) == 1 {
			rec := httptest.NewRecorder()
			httputil.NewSingleHostReverseProxy(target).ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Errorf("claim: %d %s", rec.Code, rec.Body)
			}
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	c.agent.API.URL, c.agent.Poll = proxy.URL, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- c.agent.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := c.task(t, "board-diagnose-1")
		if got.Status.Phase == cellv1.RunFailed && strings.Contains(got.Status.Result, "interrupted") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task after a lost claim %+v", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Without a model provider the agent claims no task: it would only fail.
func TestNoProvider(t *testing.T) {
	pending := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "board-diagnose-1"},
		Spec: cellv1.TaskSpec{Kind: "diagnose", App: "board", Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}}}
	c := newCell(t, func(agenttest.Conversation) (agenttest.Turn, error) {
		return agenttest.Turn{}, errors.New("the model must not run")
	}, pending)
	status := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-models-status"}, Data: map[string]string{"models.json": `{"configured":false}`}}
	if err := c.kube.Update(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	c.agent.Poll = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.task(t, "board-diagnose-1"); got.Status.Phase != "" || got.Status.Assignee != "" {
		t.Errorf("task without a provider %+v", got.Status)
	}
}

// The cell's token limit fails a task at once, with what to do; a short
// wait the provider asks for is retried, and its 429 is not the cell's limit.
func TestTokenLimit(t *testing.T) {
	var calls atomic.Int32
	var header atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		for k, v := range header.Load().(map[string]string) {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`))
	}))
	t.Cleanup(srv.Close)
	m := &Model{URL: srv.URL, Key: "test", Name: "default", HTTP: http.DefaultClient, Retries: 2}
	ask := func(h map[string]string) (int32, error) {
		calls.Store(0)
		header.Store(h)
		_, _, err := m.complete(context.Background(), []message{{Role: "user", Text: "x"}}, nil)
		return calls.Load(), err
	}
	start := time.Now()
	n, err := ask(map[string]string{"Retry-After": "3000", "X-Ratelimit-Limit": "30", "X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": "3000"})
	if err == nil || n != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("limit: %v after %d calls in %v", err, n, time.Since(start))
	}
	if got := modelError(err); !strings.Contains(got, "30 tokens per hour") || !strings.Contains(got, "in 50 minutes") {
		t.Errorf("limit message: %s", got)
	}
	n, err = ask(map[string]string{"Retry-After": "0", "X-Ratelimit-Limit": "30", "X-Ratelimit-Remaining": "5"})
	if err == nil || n != 3 || strings.Contains(modelError(err), "tokens per hour") {
		t.Errorf("provider's 429: %v after %d calls", err, n)
	}
}
