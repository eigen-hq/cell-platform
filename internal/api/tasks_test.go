package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fieldpath "k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

func pendingTask(name string) *cellv1.Task {
	return &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cellv1.TaskSpec{Kind: "diagnose", App: "notes", Input: "Ready=False", Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}}}
}

func (h *harness) steps(task string) []string {
	h.t.Helper()
	var t cellv1.Task
	if err := h.kube.Get(context.Background(), client.ObjectKey{Name: task}, &t); err != nil {
		h.t.Fatal(err)
	}
	var calls []string
	for _, s := range t.Status.Steps {
		calls = append(calls, s.Call)
	}
	return calls
}

// Agents work only in tasks they claimed, and the API records every
// call of theirs as a step before it serves it; changes they propose name
// the task.
func TestAgentsWorkInTasks(t *testing.T) {
	h := newHarness(t, installed("notes", "oidc"), installed("plain", "none"), pendingTask("notes-diagnose-1"), pendingTask("notes-diagnose-2"))

	h.agentTask = ""
	if code, body := h.do("GET", "/v0/apps/notes", "agent", nil); code != 400 || !strings.Contains(field(body, "error").(string), TaskHeader) {
		t.Errorf("agent outside a task: %d %v", code, body)
	}
	if code, _ := h.do("GET", "/v0/whoami", "agent", nil); code != 200 {
		t.Errorf("whoami outside a task: %d", code)
	}
	h.agentTask = "notes-diagnose-1"
	if code, _ := h.do("GET", "/v0/apps/notes", "agent", nil); code != 409 {
		t.Errorf("call in a task the agent has not claimed: %d", code)
	}
	if code, _ := h.do("POST", "/v0/tasks/notes-diagnose-2/claim", "admin", nil); code != 403 {
		t.Errorf("admin claimed a task: %d", code)
	}
	code, body := h.do("POST", "/v0/tasks/notes-diagnose-1/claim", "agent", nil)
	if code != 200 || field(body, "phase") != "Running" || field(body, "assignee") != "agent-user" {
		t.Fatalf("claim: %d %v", code, body)
	}
	if code, _ := h.do("POST", "/v0/tasks/notes-diagnose-1/claim", "agent", nil); code != 409 {
		t.Errorf("claimed twice: %d", code)
	}
	if code, _ := h.do("GET", "/v0/apps/notes?x=1", "agent", nil); code != 200 {
		t.Errorf("read in the task: %d", code)
	}
	code, body = h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"version": "1.1"}, "reason": "fix"})
	if code != 202 || field(body, "task") != "notes-diagnose-1" {
		t.Fatalf("proposal in the task: %d %v", code, body)
	}
	change := field(body, "name").(string)
	if got, want := h.steps("notes-diagnose-1"), []string{"GET /v0/apps/notes?x=1", "PATCH /v0/apps/notes"}; !slices.Equal(got, want) {
		t.Errorf("steps %v, want %v", got, want)
	}
	// Proposals go only to the task's app; the refused call is a step too.
	if code, _ := h.do("PATCH", "/v0/apps/plain", "agent", map[string]any{"patch": map[string]any{"version": "9"}, "reason": "x"}); code != 403 {
		t.Errorf("proposal for another app: %d", code)
	}
	// A person's proposal names no task, whatever header it sends.
	_, body = h.do("PATCH", "/v0/apps/notes", "admin", map[string]any{"patch": map[string]any{"version": "1.2"}})
	if field(body, "task") != nil {
		t.Errorf("admin change names task %v", field(body, "task"))
	}

	// Parallel calls (the model asks for several tools at once) are all
	// recorded.
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", h.srv.URL+"/v0/apps/notes/events", nil)
			req.Header.Set("Authorization", "Bearer agent")
			req.Header.Set(TaskHeader, "notes-diagnose-1")
			if resp, err := h.srv.Client().Do(req); err == nil {
				codes[i] = resp.StatusCode
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if n := len(h.steps("notes-diagnose-1")); n != 11 || slices.ContainsFunc(codes, func(c int) bool { return c != 200 }) {
		t.Errorf("parallel calls: %v, %d steps", codes, n)
	}

	// Tasks hold what agents read (logs in results): for admins and agents only.
	for _, path := range []string{"/v0/tasks", "/v0/tasks/notes-diagnose-1"} {
		for _, role := range []string{"none", "viewer"} {
			if code, _ := h.do("GET", path, role, nil); code != 403 {
				t.Errorf("%s reads %s: %d", role, path, code)
			}
		}
	}
	_, body = h.do("GET", "/v0/tasks/notes-diagnose-1", "admin", nil)
	if ch := field(body, "changes").([]any); len(ch) != 1 || ch[0] != change || len(field(body, "steps").([]any)) != 11 {
		t.Errorf("task view %v", body)
	}
	if code, _ := h.do("POST", "/v0/tasks/notes-diagnose-1/finish", "admin", map[string]any{"result": "x"}); code != 409 {
		t.Errorf("admin finished the agent's task: %d", code)
	}
	code, body = h.do("POST", "/v0/tasks/notes-diagnose-1/finish", "agent", map[string]any{"result": "the version was wrong"})
	if code != 200 || field(body, "phase") != "Succeeded" || field(body, "result") != "the version was wrong" {
		t.Fatalf("finish: %d %v", code, body)
	}
	if code, _ := h.do("GET", "/v0/apps/notes", "agent", nil); code != 409 {
		t.Errorf("call in a finished task: %d", code)
	}
	if code, _ := h.do("POST", "/v0/tasks/notes-diagnose-1/finish", "agent", map[string]any{"result": "again"}); code != 409 {
		t.Errorf("finished twice: %d", code)
	}
	h.do("POST", "/v0/tasks/notes-diagnose-2/claim", "agent", nil)
	h.agentTask = "notes-diagnose-2"
	code, body = h.do("POST", "/v0/tasks/notes-diagnose-2/finish", "agent", map[string]any{"result": "no model", "failed": true})
	if code != 200 || field(body, "phase") != "Failed" {
		t.Errorf("failed finish: %d %v", code, body)
	}
	_, body = h.do("GET", "/v0/tasks?phase=Running", "admin", nil)
	if names := names(body); !slices.Equal(names, []string{agentWork}) {
		t.Errorf("running tasks %v", names)
	}

	// A runaway agent stops at the step limit.
	full := pendingTask("notes-diagnose-3")
	full.Status = cellv1.TaskStatus{Phase: cellv1.RunRunning, Assignee: "agent-user", Steps: make([]cellv1.TaskStep, maxSteps)}
	if err := h.kube.Create(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	if err := h.kube.Status().Update(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	h.agentTask = full.Name
	if code, body := h.do("GET", "/v0/apps/notes", "agent", nil); code != 409 || !strings.Contains(field(body, "error").(string), "finish it") {
		t.Errorf("call past the step limit: %d %v", code, body)
	}
}

func names(list any) []string {
	var out []string
	for _, x := range list.([]any) {
		out = append(out, field(x, "name").(string))
	}
	return out
}

func TestCreateTask(t *testing.T) {
	h := newHarness(t, installed("notes", "oidc"))
	compose := "services:\n  memo:\n    image: example/memo:1.0\n"
	for _, c := range []struct {
		token string
		req   map[string]any
		code  int
	}{
		{"viewer", map[string]any{"kind": "diagnose", "app": "notes"}, 403},
		{"agent", map[string]any{"kind": "diagnose", "app": "notes"}, 403},
		{"admin", map[string]any{"kind": "diagnose", "app": "gone"}, 404},
		{"admin", map[string]any{"kind": "package", "app": "memo"}, 400},
		{"admin", map[string]any{"kind": "package", "app": "notes", "input": compose}, 409},
		{"admin", map[string]any{"kind": "repair", "app": "notes"}, 400},
		{"admin", map[string]any{"kind": "ask", "input": " "}, 400},
		{"admin", map[string]any{"kind": "ask", "input": strings.Repeat("\u00e4", maxAsk+1)}, 400},
		{"admin", map[string]any{"kind": "ask", "app": "gone", "input": "why is it slow?"}, 404},
		{"viewer", map[string]any{"kind": "ask", "input": "why is it slow?"}, 403},
	} {
		if code, body := h.do("POST", "/v0/tasks", c.token, c.req); code != c.code {
			t.Errorf("%s %v: %d %v, want %d", c.token, c.req, code, body, c.code)
		}
	}
	code, body := h.do("POST", "/v0/tasks", "admin", map[string]any{"kind": "package", "app": "memo", "input": compose})
	if code != 201 || field(body, "phase") != "Pending" || field(body, "requester", "name") != "admin-user" || field(body, "input") != compose {
		t.Fatalf("package task: %d %v", code, body)
	}
	_, body = h.do("GET", "/v0/tasks?app=memo", "admin", nil)
	if len(body.([]any)) != 1 {
		t.Errorf("tasks of memo: %v", body)
	}
	// The limit counts characters, not bytes.
	if code, _ := h.do("POST", "/v0/tasks", "admin", map[string]any{"kind": "ask", "app": "notes", "input": strings.Repeat("\u00e4", maxAsk)}); code != 201 {
		t.Errorf("ask of %d two-byte characters: %d", maxAsk, code)
	}
	code, body = h.do("POST", "/v0/tasks", "admin", map[string]any{"kind": "ask", "app": "notes", "input": "why is notes slow?"})
	if code != 201 || !strings.HasPrefix(field(body, "name").(string), "notes-ask-") {
		t.Errorf("ask about an app: %d %v", code, body)
	}
	// An ask about the cell sees the cell, not the inside of an app.
	code, body = h.do("POST", "/v0/tasks", "admin", map[string]any{"kind": "ask", "input": "which machine is fullest?"})
	if code != 201 || field(body, "app") != "" || !strings.HasPrefix(field(body, "name").(string), "ask-") {
		t.Fatalf("ask about the cell: %d %v", code, body)
	}
	ask := field(body, "name").(string)
	if code, _ := h.do("POST", "/v0/tasks/"+ask+"/claim", "agent", nil); code != 200 {
		t.Fatalf("claim ask: %d", code)
	}
	h.agentTask = ask
	if code, _ := h.do("GET", "/v0/apps", "agent", nil); code != 200 {
		t.Errorf("ask lists apps: %d", code)
	}
	if code, _ := h.do("GET", "/v0/apps/notes/logs", "agent", nil); code != 403 {
		t.Errorf("ask about the cell reads an app's logs: %d", code)
	}
	if code, _ := h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"version": "2"}, "reason": "x"}); code != 403 {
		t.Errorf("ask about the cell proposes a change: %d", code)
	}
}

// The metrics API's usage lands on the container of the same pod and name.
func TestPodUsage(t *testing.T) {
	pods := []podView{{Name: "notes-0", Containers: []containerView{{Name: "notes"}, {Name: "proxy"}}}}
	metrics := []unstructured.Unstructured{
		{Object: map[string]any{"metadata": map[string]any{"name": "notes-0"}, "containers": []any{
			map[string]any{"name": "notes", "usage": map[string]any{"cpu": "250m", "memory": "100Mi"}},
		}}},
		{Object: map[string]any{"metadata": map[string]any{"name": "gone-0"}, "containers": []any{
			map[string]any{"name": "notes", "usage": map[string]any{"cpu": "1", "memory": "1Gi"}},
		}}},
	}
	podUsage(pods, metrics)
	c := pods[0].Containers
	if c[0].CPUUsed == nil || *c[0].CPUUsed != 0.25 || c[0].MemoryUsed == nil || *c[0].MemoryUsed != 100<<20 {
		t.Errorf("notes: %+v", c[0])
	}
	if c[1].CPUUsed != nil || c[1].MemoryUsed != nil {
		t.Errorf("unmeasured proxy: %+v", c[1])
	}
}

// What admins and agents look at when an app is in trouble.
func TestDiagnosisReads(t *testing.T) {
	old := metav1.NewTime(time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC))
	pod := func(name string, created metav1.Time) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app-notes", CreationTimestamp: created},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "notes", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi"), corev1.ResourceCPU: resource.MustParse("500m")}}}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				Name: "notes", Image: "example/notes:1.0", RestartCount: 3,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: old}},
			}}}}
	}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e1", Namespace: "app-notes"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "notes-new"}, Type: "Warning", Reason: "BackOff",
		Message: "Back-off restarting failed container", Count: 5, LastTimestamp: old}
	other := event.DeepCopy()
	other.Namespace, other.Name = "app-other", "e2"
	h := newHarness(t, installed("notes", "oidc"), pod("notes-old", old), pod("notes-new", metav1.NewTime(old.Add(time.Hour))), event, other)

	if code, _ := h.do("GET", "/v0/apps/notes/pods", "none", nil); code != 403 {
		t.Errorf("no role reads pods: %d", code)
	}
	code, body := h.do("GET", "/v0/apps/notes/pods", "agent", nil)
	if code != 200 || len(body.([]any)) != 2 || field(body.([]any)[0], "name") != "notes-new" {
		t.Fatalf("pods: %d %v", code, body)
	}
	c := field(body.([]any)[0], "containers").([]any)[0]
	if field(c, "reason") != "CrashLoopBackOff" || field(c, "restarts") != 3.0 || field(c, "lastExit", "exitCode") != 1.0 ||
		field(c, "memoryRequest") != float64(64<<20) || field(c, "memoryLimit") != float64(128<<20) || field(c, "cpuLimit") != 0.5 || field(c, "cpuRequest") != nil {
		t.Errorf("container %v", c)
	}
	code, body = h.do("GET", "/v0/apps/notes/events", "admin", nil)
	if code != 200 || len(body.([]any)) != 1 || field(body.([]any)[0], "object") != "Pod/notes-new" {
		t.Errorf("events: %d %v", code, body)
	}
	// The fake clientset answers every log request with "fake logs".
	if code, body := h.do("GET", "/v0/apps/notes/logs?tail=10&previous=true", "agent", nil); code != 200 || body != "fake logs" {
		t.Errorf("logs: %d %v", code, body)
	}
	for path, want := range map[string]int{
		"/v0/apps/notes/logs?pod=nope": 404, "/v0/apps/notes/logs?tail=0": 400,
		"/v0/apps/notes/logs?previous=maybe": 400,
		// Only the app the agent's task is about.
		"/v0/apps/gone/logs": 403, "/v0/apps/other/pods": 403, "/v0/apps/other/events": 403, "/v0/apps/other": 403,
	} {
		if code, _ := h.do("GET", path, "agent", nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

// An app from elsewhere (an agent packaged it) installs from its
// manifest. The cluster checks it first, with a dry run, so a wrong
// manifest is refused before anyone approves it.
func TestInstallFromManifest(t *testing.T) {
	var created []string
	h := newHarnessWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		app, ok := obj.(*cellv1.App)
		if !ok {
			return c.Create(ctx, obj, opts...)
		}
		o := &client.CreateOptions{}
		o.ApplyOptions(opts)
		if !slices.Contains(o.DryRun, metav1.DryRunAll) {
			created = append(created, app.Name)
		}
		if app.Spec.Access.Subdomain == "api" {
			return apierrors.NewInvalid(schema.GroupKind{Group: "cell.internal", Kind: "App"}, app.Name,
				fieldpath.ErrorList{fieldpath.Invalid(fieldpath.NewPath("spec", "access"), "api", "subdomains auth and api belong to the cell")})
		}
		return c.Create(ctx, obj, opts...)
	}}, installed("notes", "oidc"), &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "memo-package-1"},
		Spec:   cellv1.TaskSpec{Kind: "package", App: "memo", Input: "services: {}", Requester: cellv1.Requester{Kind: "Human", Name: "admin-user"}},
		Status: cellv1.TaskStatus{Phase: cellv1.RunRunning, Assignee: "agent-user"}})
	h.agentTask = "memo-package-1"
	spec := map[string]any{"version": "1.0", "source": map[string]any{"image": map[string]any{"repository": "example/memo"}},
		"needs":  map[string]any{"volumes": []any{map[string]any{"name": "data", "size": "1Gi", "mountPath": "/data"}}},
		"access": map[string]any{"subdomain": "memo", "port": 5230, "auth": map[string]any{"mode": "proxy"}}}
	bad := func(mutate func(map[string]any)) map[string]any {
		m := map[string]any{}
		for k, v := range spec {
			m[k] = v
		}
		mutate(m)
		return m
	}
	for name, req := range map[string]map[string]any{
		"both":          {"catalog": "notes", "manifest": spec, "name": "memo"},
		"neither":       {"name": "memo"},
		"no name":       {"manifest": spec},
		"unknown field": {"manifest": bad(func(m map[string]any) { m["ports"] = []any{"5230:5230"} }), "name": "memo"},
		"cluster rule": {"manifest": bad(func(m map[string]any) {
			m["access"] = map[string]any{"subdomain": "api", "port": 80, "auth": map[string]any{"mode": "none"}}
		}), "name": "memo"},
	} {
		if code, body := h.do("POST", "/v0/apps", "agent", req); code != 400 {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	code, body := h.do("POST", "/v0/apps", "agent", map[string]any{"manifest": spec, "name": "memo"})
	if code != 202 || field(body, "risk") != "high" || field(body, "action") != "install" ||
		field(body, "patch", "access", "port") != 5230.0 || !strings.Contains(field(body, "reason").(string), "not in the catalog") {
		t.Fatalf("install from a manifest: %d %v", code, body)
	}
	// A manifest file as people write it: the name comes from its
	// metadata, the spec goes through the same checks.
	file := "apiVersion: cell.internal/v1alpha1\nkind: App\nmetadata:\n  name: memo2\nspec:\n  version: \"1.0\"\n" +
		"  source:\n    image:\n      repository: example/memo\n  access:\n    subdomain: memo2\n    port: 5230\n    auth:\n      mode: proxy\n"
	for name, req := range map[string]map[string]any{
		"both":                             {"manifest": spec, "manifestYaml": file},
		"not yaml":                         {"manifestYaml": "spec: [unclosed"},
		"not an app":                       {"manifestYaml": "services:\n  memo:\n    image: example/memo\n"},
		"no spec":                          {"manifestYaml": "apiVersion: cell.internal/v1alpha1\nkind: App\nmetadata:\n  name: memo2\n"},
		"unknown spec":                     {"manifestYaml": file + "  ports: [\"5230:5230\"]\n"},
		"repeated key":                     {"manifestYaml": file + "  access:\n    subdomain: memo3\n    port: 5230\n"},
		"two documents":                    {"manifestYaml": file + "---\n" + file},
		"two documents, old Mac line ends": {"manifestYaml": strings.ReplaceAll(file+"---\n"+file, "\n", "\r")},
		"unknown top key":                  {"manifestYaml": file + "health:\n  path: /health\n"},
		"unknown metadata":                 {"manifestYaml": strings.Replace(file, "  name: memo2\n", "  name: memo2\n  lables: {a: b}\n", 1)},
	} {
		if code, body := h.do("POST", "/v0/apps", "admin", req); code != 400 {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	code, body = h.do("POST", "/v0/apps", "admin", map[string]any{"manifestYaml": file})
	if code != 202 || field(body, "app") != "memo2" || field(body, "risk") != "high" || field(body, "patch", "access", "port") != 5230.0 {
		t.Fatalf("install from a manifest file: %d %v", code, body)
	}
	// An App as kubectl prints it installs: labels, status and the rest.
	exported := strings.NewReplacer("name: memo2", "name: memo4\n  labels: {team: ops}\n  resourceVersion: \"7\"", "subdomain: memo2", "subdomain: memo4").Replace(file) + "status:\n  phase: Ready\n"
	if code, body := h.do("POST", "/v0/apps", "admin", map[string]any{"manifestYaml": exported}); code != 202 {
		t.Fatalf("install from an exported App: %d %v", code, body)
	}
	// A date as the version stays the text it was, as kubectl reads it.
	dated := strings.NewReplacer("name: memo2", "name: memo3", "subdomain: memo2", "subdomain: memo3", `version: "1.0"`, "version: 2026-11-27").Replace(file)
	code, body = h.do("POST", "/v0/apps", "admin", map[string]any{"manifestYaml": dated})
	if code != 202 || field(body, "patch", "version") != "2026-11-27" {
		t.Fatalf("install from a manifest file with a date as version: %d %v", code, body)
	}
	if code, _ := h.do("POST", "/v0/apps", "admin", map[string]any{"catalog": "notes", "name": "notes2", "subdomain": "notes2"}); code != 202 {
		t.Errorf("catalog install: %d", code)
	}
	if len(created) != 0 {
		t.Errorf("the API created apps: %v", created)
	}
}
