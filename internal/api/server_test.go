package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/authentik"
)

// --- test doubles -----------------------------------------------------------

type fakeIdentity struct {
	mu     sync.Mutex
	users  map[string]authentik.User
	groups map[string][]string
}

func newFakeIdentity() *fakeIdentity {
	return &fakeIdentity{users: map[string]authentik.User{}, groups: map[string][]string{GroupAdmins: {"akadmin"}}}
}

func (f *fakeIdentity) ListUsers(context.Context) ([]authentik.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []authentik.User
	for _, u := range f.users {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeIdentity) CreateUser(_ context.Context, u authentik.User, _ string) (authentik.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[u.Username]; ok {
		return u, &authentik.Error{Status: http.StatusBadRequest, Body: `{"username":["user with this username already exists."]}`}
	}
	f.users[u.Username] = u
	return u, nil
}

func (f *fakeIdentity) CreateAppPassword(_ context.Context, username string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[username]; !ok {
		return "", fmt.Errorf("user %q: %w", username, authentik.ErrNotFound)
	}
	return "app-password-" + username, nil
}

func (f *fakeIdentity) ListGroups(context.Context) ([]authentik.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []authentik.Group
	for name, members := range f.groups {
		out = append(out, authentik.Group{Name: name, Members: members})
	}
	return out, nil
}

func (f *fakeIdentity) CreateGroup(_ context.Context, name string) (authentik.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups[name] = nil
	return authentik.Group{Name: name}, nil
}

func (f *fakeIdentity) ChangeGroupMembers(_ context.Context, group string, add, remove []string) (authentik.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	members, ok := f.groups[group]
	if !ok {
		return authentik.Group{}, fmt.Errorf("group %q: %w", group, authentik.ErrNotFound)
	}
	for _, u := range add {
		if !slices.Contains(members, u) {
			members = append(members, u)
		}
	}
	members = slices.DeleteFunc(members, func(u string) bool { return slices.Contains(remove, u) })
	f.groups[group] = members
	return authentik.Group{Name: group, Members: slices.Clone(members)}, nil
}

const manifest = `apiVersion: cell.internal/v1alpha1
kind: App
metadata:
  name: notes
spec:
  title: Notes
  version: "1.0"
  source:
    image:
      repository: example/notes
  access:
    subdomain: notes
    port: 80
    auth:
      mode: oidc
`

func installed(name, mode string) *cellv1.App {
	return &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cellv1.AppSpec{Title: "Notes", Version: "1.0",
			Source: cellv1.Source{Image: &cellv1.ImageSource{Repository: "example/notes"}},
			Access: cellv1.Access{Subdomain: name, Port: 80, Auth: cellv1.Auth{Mode: mode}}},
		Status: cellv1.AppStatus{Phase: cellv1.PhaseReady, Version: "1.0", URL: "https://" + name + ".cell.localhost",
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Healthy", Message: "ok", LastTransitionTime: metav1.Now()}}},
	}
}

// harness is a Server over a fake cluster, checked against the contract.
type harness struct {
	t      *testing.T
	kube   client.WithWatch
	srv    *httptest.Server
	router routers.Router
	server *Server
	// agentTask is the task the agent's calls name ("" for none).
	agentTask string
}

// agentWork is the running task the harness's agent works in.
const agentWork = "agent-work"

func newHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	return newHarnessWith(t, interceptor.Funcs{}, objs...)
}

// newHarnessWith lets the fake cluster misbehave (funcs).
func newHarnessWith(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *harness {
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
	work := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: agentWork},
		Spec:   cellv1.TaskSpec{Kind: "diagnose", App: "notes", Requester: cellv1.Requester{Kind: "Human", Name: "admin-user"}},
		Status: cellv1.TaskStatus{Phase: cellv1.RunRunning, Assignee: "agent-user"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, work)...).WithInterceptorFuncs(funcs).
		WithStatusSubresource(&cellv1.Task{}).Build()
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Kube:     kube,
		Pods:     k8sfake.NewSimpleClientset(pods...).CoreV1(),
		Identity: newFakeIdentity(),
		Authenticate: func(_ context.Context, token string) (*Caller, error) {
			switch token {
			case "admin", "agent", "viewer", "none":
				return &Caller{Username: token + "-user", Role: token, Groups: []string{}}, nil
			}
			return nil, errors.New("bad token")
		},
		Catalog: fstest.MapFS{"notes/manifest.yaml": {Data: []byte(manifest)}},
		OpenAPI: spec,
		Now:     func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC) },
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("openapi.yaml is not valid: %v", err)
	}
	doc.Servers = openapi3.Servers{{URL: "/"}}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, kube: kube, srv: httptest.NewServer(s.Handler()), router: router, server: s, agentTask: agentWork}
	t.Cleanup(h.srv.Close)
	return h
}

// do sends a request and checks request and response against the OpenAPI
// document. It returns the status and the decoded body.
func (h *harness) do(method, path, token string, body any) (int, any) {
	h.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if token == "agent" && h.agentTask != "" {
		req.Header.Set(TaskHeader, h.agentTask)
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	h.validate(req, raw, resp, data)
	var out any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &out); err != nil {
			h.t.Fatalf("%s %s: invalid JSON %q", method, path, data)
		}
	} else {
		out = string(data)
	}
	return resp.StatusCode, out
}

func (h *harness) validate(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte) {
	h.t.Helper()
	check := httptest.NewRequest(req.Method, req.URL.RequestURI(), bytes.NewReader(reqBody))
	check.Header = req.Header
	route, params, err := h.router.FindRoute(check)
	if err != nil {
		h.t.Fatalf("%s %s is not in openapi.yaml: %v", req.Method, req.URL.Path, err)
	}
	in := &openapi3filter.RequestValidationInput{Request: check, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if resp.StatusCode < 400 {
		if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil {
			h.t.Errorf("%s %s: request does not match openapi.yaml: %v", req.Method, req.URL.Path, err)
		}
	}
	json := strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json")
	out := &openapi3filter.ResponseValidationInput{RequestValidationInput: in, Status: resp.StatusCode, Header: resp.Header,
		Options: &openapi3filter.Options{IncludeResponseStatus: true, ExcludeResponseBody: !json}}
	out.SetBodyBytes(respBody)
	if err := openapi3filter.ValidateResponse(context.Background(), out); err != nil {
		h.t.Errorf("%s %s: response %d does not match openapi.yaml: %v\n%s", req.Method, req.URL.Path, resp.StatusCode, err, respBody)
	}
}

func field(v any, path ...string) any {
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	return v
}

// --- contract ---------------------------------------------------------------

// The OpenAPI file is the contract. Every route is documented, every
// documented operation is served, and every answer in these tests matches
// its schema (see harness.validate).
func TestRoutesMatchOpenAPI(t *testing.T) {
	h := newHarness(t)
	served := map[string]bool{}
	for _, rt := range h.server.routes() {
		served[rt.method+" "+rt.path] = true
	}
	doc, err := openapi3.NewLoader().LoadFromData(h.server.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented[method+" "+path] = true
		}
	}
	for op := range served {
		if !documented[op] {
			t.Errorf("%s is served but not in openapi.yaml", op)
		}
	}
	for op := range documented {
		if !served[op] {
			t.Errorf("%s is in openapi.yaml but not served", op)
		}
	}
	if code, body := h.do("GET", "/openapi.yaml", "", nil); code != 200 || !strings.Contains(body.(string), "openapi: 3.0.3") {
		t.Errorf("openapi.yaml: %d", code)
	}
}

func TestAuthentication(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.do("GET", "/healthz", "", nil); code != 200 {
		t.Errorf("healthz: %d", code)
	}
	if code, _ := h.do("GET", "/v0/apps", "", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := h.do("GET", "/v0/apps", "forged", nil); code != 401 {
		t.Errorf("bad token: %d", code)
	}
	if code, body := h.do("GET", "/v0/whoami", "agent", nil); code != 200 || field(body, "role") != "agent" {
		t.Errorf("whoami: %d %v", code, body)
	}
	if code, _ := h.do("POST", "/v0/apps", "viewer", map[string]any{"catalog": "notes"}); code != 403 {
		t.Errorf("viewer installs: %d", code)
	}
	if code, _ := h.do("GET", "/v0/users", "viewer", nil); code != 403 {
		t.Errorf("viewer lists users: %d", code)
	}
	// An account in no role group logs in, learns why it sees nothing.
	if code, body := h.do("GET", "/v0/whoami", "none", nil); code != 200 || field(body, "role") != "none" {
		t.Errorf("whoami without role: %d %v", code, body)
	}
	if code, body := h.do("GET", "/v0/apps", "none", nil); code != 403 || !strings.Contains(fmt.Sprint(body), "cell-viewers") {
		t.Errorf("no role lists apps: %d %v", code, body)
	}
}

func TestRoleOf(t *testing.T) {
	for groups, want := range map[string]string{"": RoleNone, "staff": RoleNone, "staff cell-viewers": RoleViewer, "cell-viewers cell-agents": RoleViewer, "cell-agents": RoleAgent, "staff cell-admins cell-agents": RoleAdmin} {
		if got := RoleOf(strings.Fields(groups)); got != want {
			t.Errorf("%q: %s, want %s", groups, got, want)
		}
	}
}

// --- apps and changes -------------------------------------------------------

// Changes go through the API and wait for an admin's decision in a
// call of its own; the requester cannot set the approval.
func TestInstallWaitsForApproval(t *testing.T) {
	h := newHarness(t)
	code, body := h.do("GET", "/v0/catalog", "viewer", nil)
	if code != 200 || len(body.([]any)) != 1 || field(body.([]any)[0], "auth") != "oidc" {
		t.Fatalf("catalog: %d %v", code, body)
	}
	if code, body = h.do("GET", "/v0/catalog/notes", "viewer", nil); code != 200 || field(body, "manifest", "version") != "1.0" {
		t.Errorf("catalog entry: %d %v", code, body)
	}
	if code, _ = h.do("GET", "/v0/catalog/nope", "viewer", nil); code != 404 {
		t.Errorf("missing catalog entry: %d", code)
	}

	code, body = h.do("POST", "/v0/apps", "agent", map[string]any{"catalog": "notes", "groups": []string{"staff"}})
	if code != 202 {
		t.Fatalf("install: %d %v", code, body)
	}
	name := field(body, "name").(string)
	if field(body, "action") != "install" || field(body, "requester", "kind") != "Agent" || field(body, "requester", "name") != "agent-user" ||
		field(body, "approval", "required") != true || field(body, "approval", "decision") != nil {
		t.Errorf("recorded change %v", body)
	}
	if g := field(body, "patch", "access", "groups").([]any); len(g) != 1 || g[0] != "staff" {
		t.Errorf("install spec %v", field(body, "patch"))
	}

	// Approval cannot ride along with a proposal.
	if code, _ = h.do("POST", "/v0/apps", "admin", map[string]any{"catalog": "notes", "approval": map[string]any{"decision": "Approved"}}); code != 400 {
		t.Errorf("proposal with approval: %d", code)
	}
	if code, _ = h.do("POST", "/v0/changes/"+name+"/approve", "agent", nil); code != 403 {
		t.Errorf("agent approves: %d", code)
	}
	code, body = h.do("POST", "/v0/changes/"+name+"/approve", "admin", nil)
	if code != 200 || field(body, "approval", "decision") != "Approved" || field(body, "approval", "by") != "admin-user" || field(body, "approval", "at") == nil {
		t.Errorf("approve: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/changes/"+name+"/reject", "admin", nil); code != 409 {
		t.Errorf("second decision: %d", code)
	}
	var cr cellv1.ChangeRequest
	if err := h.kube.Get(context.Background(), client.ObjectKey{Name: name}, &cr); err != nil || cr.Spec.Approval.Decision != "Approved" || cr.Spec.Approval.By != "admin-user" {
		t.Errorf("stored decision %+v %v", cr.Spec.Approval, err)
	}
	if code, body = h.do("GET", "/v0/changes/"+name, "viewer", nil); code != 200 || field(body, "name") != name {
		t.Errorf("get change: %d %v", code, body)
	}
	if code, _ = h.do("GET", "/v0/changes/nope", "viewer", nil); code != 404 {
		t.Errorf("missing change: %d", code)
	}
}

// The operator writes a new change at once (finalizer): an approval that
// races it is retried, not refused.
func TestApprovalRetriesConflicts(t *testing.T) {
	conflicts := 2
	h := newHarnessWith(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if _, ok := obj.(*cellv1.ChangeRequest); ok && conflicts > 0 {
			conflicts--
			return apierrors.NewConflict(schema.GroupResource{Group: "cell.internal", Resource: "changerequests"}, obj.GetName(), errors.New("modified"))
		}
		return c.Update(ctx, obj, opts...)
	}}, installed("notes", "oidc"))
	_, body := h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"version": "2"}})
	if code, body := h.do("POST", "/v0/changes/"+field(body, "name").(string)+"/approve", "admin", nil); code != 200 || conflicts != 0 {
		t.Errorf("approve during a concurrent write: %d %v", code, body)
	}
}

func TestInstallConflictsAndRejection(t *testing.T) {
	h := newHarness(t, installed("notes", "oidc"))
	if code, _ := h.do("POST", "/v0/apps", "admin", map[string]any{"catalog": "notes"}); code != 409 {
		t.Errorf("install over an installed app: %d", code)
	}
	for _, name := range []string{"Notes_2", "cell-api"} {
		if code, _ := h.do("POST", "/v0/apps", "admin", map[string]any{"catalog": "notes", "name": name}); code != 400 {
			t.Errorf("name %s: %d", name, code)
		}
	}
	// Another name on the same host would take over the app's traffic.
	if code, _ := h.do("POST", "/v0/apps", "agent", map[string]any{"catalog": "notes", "name": "notes2"}); code != 409 {
		t.Errorf("install on a taken subdomain: %d", code)
	}
	code, body := h.do("POST", "/v0/apps", "admin", map[string]any{"catalog": "notes", "name": "notes2", "subdomain": "notes2"})
	if code != 202 || field(body, "app") != "notes2" || field(body, "patch", "access", "subdomain") != "notes2" || field(body, "risk") != "low" {
		t.Fatalf("install under another name: %d %v", code, body)
	}
	// Data a removed app left behind goes to whatever is installed under
	// its name: approvers see that.
	if err := h.kube.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-old"}}); err != nil {
		t.Fatal(err)
	}
	code, body = h.do("POST", "/v0/apps", "admin", map[string]any{"catalog": "notes", "name": "old", "subdomain": "old"})
	if code != 202 || field(body, "risk") != "high" || !strings.Contains(field(body, "reason").(string), "namespace app-old") {
		t.Errorf("install over retained data: %d %v", code, body)
	}
	code, body = h.do("POST", "/v0/changes/"+field(body, "name").(string)+"/reject", "admin", nil)
	if code != 200 || field(body, "approval", "decision") != "Rejected" {
		t.Errorf("reject: %d %v", code, body)
	}
}

func TestAppChanges(t *testing.T) {
	done := metav1.NewTime(time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC))
	old := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "notes-old", CreationTimestamp: metav1.NewTime(done.Add(-time.Hour))},
		Spec: cellv1.BackupSpec{App: "notes"}, Status: cellv1.BackupStatus{Phase: cellv1.RunCompleted, CompletedAt: &done}}
	newer := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "notes-new", CreationTimestamp: metav1.NewTime(done.Add(time.Hour))},
		Spec: cellv1.BackupSpec{App: "notes"}, Status: cellv1.BackupStatus{Phase: cellv1.RunCompleted, CompletedAt: &done}}
	running := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "notes-running", CreationTimestamp: metav1.NewTime(done.Add(2 * time.Hour))},
		Spec: cellv1.BackupSpec{App: "notes"}, Status: cellv1.BackupStatus{Phase: cellv1.RunRunning}}
	h := newHarness(t, installed("notes", "oidc"), installed("plain", "none"), old, newer, running)

	code, body := h.do("GET", "/v0/apps", "viewer", nil)
	if code != 200 || len(body.([]any)) != 2 || field(body.([]any)[0], "url") != "https://notes.cell.localhost" {
		t.Fatalf("apps: %d %v", code, body)
	}
	if code, _ = h.do("GET", "/v0/apps/nope", "viewer", nil); code != 404 {
		t.Errorf("missing app: %d", code)
	}

	// Update: a merge patch, checked against the manifest's fields.
	code, body = h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"version": "1.1"}, "reason": "security fix"})
	if code != 202 || field(body, "action") != "update" || field(body, "patch", "version") != "1.1" || field(body, "risk") != "medium" {
		t.Errorf("update: %d %v", code, body)
	}
	if code, _ = h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"versoin": "1.1"}}); code != 400 {
		t.Errorf("patch with a typo: %d", code)
	}
	// The API rates the risk; the requester cannot play it down.
	if code, _ = h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"version": "1.1"}, "risk": "low"}); code != 400 {
		t.Errorf("requester sets the risk: %d", code)
	}
	for _, patch := range []map[string]any{
		{"source": map[string]any{"image": map[string]any{"repository": "evil/notes"}}},
		{"version": "1.2", "health": map[string]any{"timeoutSeconds": 10}},
		{"access": map[string]any{"subdomain": "free"}},
	} {
		if code, body = h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": patch}); code != 202 || field(body, "risk") != "high" {
			t.Errorf("patch %v: %d %v", patch, code, body)
		}
	}
	if code, _ = h.do("PATCH", "/v0/apps/notes", "agent", map[string]any{"patch": map[string]any{"access": map[string]any{"subdomain": "plain"}}}); code != 409 {
		t.Errorf("move onto a taken subdomain: %d", code)
	}

	// Access: a low-risk update; an app without login cannot have groups.
	code, body = h.do("PUT", "/v0/apps/notes/access", "agent", map[string]any{"groups": []string{"finance"}})
	if code != 202 || field(body, "risk") != "low" || field(body, "patch", "access", "groups").([]any)[0] != "finance" {
		t.Errorf("set access: %d %v", code, body)
	}
	if code, _ = h.do("PUT", "/v0/apps/plain/access", "admin", map[string]any{"groups": []string{"finance"}}); code != 400 {
		t.Errorf("groups without login: %d", code)
	}
	// A forgotten or null list never opens the app to everyone.
	for _, req := range []any{map[string]any{"reason": "oops"}, map[string]any{"groups": nil}} {
		if code, _ = h.do("PUT", "/v0/apps/notes/access", "agent", req); code != 400 {
			t.Errorf("access %v: %d", req, code)
		}
	}
	if code, body = h.do("GET", "/v0/apps/notes/access", "viewer", nil); code != 200 || len(field(body, "groups").([]any)) != 0 {
		t.Errorf("access before the change ran: %d %v", code, body)
	}

	// Restore: the newest completed backup unless one is named.
	code, body = h.do("POST", "/v0/apps/notes/restore", "agent", map[string]any{})
	if code != 202 || field(body, "restores") != "notes-new" || field(body, "risk") != "high" {
		t.Errorf("restore latest: %d %v", code, body)
	}
	if code, body = h.do("POST", "/v0/apps/notes/restore", "agent", map[string]any{"backup": "notes-old"}); code != 202 || field(body, "restores") != "notes-old" {
		t.Errorf("restore named: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/apps/plain/restore", "admin", map[string]any{}); code != 400 {
		t.Errorf("restore without backups: %d", code)
	}

	// Restart: a low-risk change; an agent only for its task's app.
	for _, token := range []string{"none", "viewer"} {
		if code, _ = h.do("POST", "/v0/apps/notes/restart", token, map[string]any{}); code != 403 {
			t.Errorf("restart as %s: %d", token, code)
		}
	}
	if code, _ = h.do("POST", "/v0/apps/plain/restart", "agent", map[string]any{}); code != 403 {
		t.Errorf("agent restarts another app: %d", code)
	}
	code, body = h.do("POST", "/v0/apps/notes/restart", "agent", map[string]any{"reason": "hangs"})
	if code != 202 || field(body, "action") != "restart" || field(body, "risk") != "low" || field(body, "reason") != "hangs" || field(body, "patch") != nil {
		t.Errorf("restart: %d %v", code, body)
	}
	if code, body = h.do("POST", "/v0/apps/notes/restart", "admin", map[string]any{}); code != 202 || field(body, "reason") != "restart notes" {
		t.Errorf("restart as admin: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/apps/plain/restart", "admin", map[string]any{"patch": map[string]any{}}); code != 400 {
		t.Errorf("restart with an unknown field: %d", code)
	}

	// Remove: keeps the data unless asked.
	code, body = h.do("DELETE", "/v0/apps/notes?deleteData=true&reason=gone", "agent", nil)
	if code != 202 || field(body, "action") != "remove" || field(body, "deleteData") != true || field(body, "reason") != "gone" {
		t.Errorf("remove: %d %v", code, body)
	}
	if code, _ = h.do("DELETE", "/v0/apps/notes?deleteData=maybe", "agent", nil); code != 400 {
		t.Errorf("bad deleteData: %d", code)
	}

	// The change log, newest first and per app.
	code, body = h.do("GET", "/v0/changes?app=notes", "viewer", nil)
	if code != 200 || len(body.([]any)) != 10 {
		t.Errorf("change log: %d %v", code, body)
	}
	if code, body = h.do("GET", "/v0/changes?app=plain", "viewer", nil); code != 200 || len(body.([]any)) != 0 {
		t.Errorf("change log of plain: %d %v", code, body)
	}
}

func TestBackups(t *testing.T) {
	h := newHarness(t, installed("notes", "oidc"))
	if code, _ := h.do("POST", "/v0/apps/notes/backups", "agent", nil); code != 403 {
		t.Errorf("agent backs up: %d", code)
	}
	code, body := h.do("POST", "/v0/apps/notes/backups", "admin", nil)
	if code != 201 || field(body, "name") != "notes-20261119-120000" || field(body, "reason") != "manual, by admin-user" {
		t.Fatalf("backup: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/apps/notes/backups", "admin", nil); code != 409 {
		t.Errorf("same second: %d", code)
	}
	if code, body = h.do("GET", "/v0/apps/notes/backups", "viewer", nil); code != 200 || len(body.([]any)) != 1 {
		t.Errorf("backups: %d %v", code, body)
	}
}

// --- identity ---------------------------------------------------------------

func TestIdentity(t *testing.T) {
	h := newHarness(t)
	code, body := h.do("POST", "/v0/users", "admin", map[string]any{"username": "alice", "name": "Alice", "email": "a@example.org", "password": "pw"})
	if code != 201 || field(body, "username") != "alice" {
		t.Fatalf("create user: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/users", "admin", map[string]any{"username": "alice", "name": "Alice"}); code != 409 {
		t.Errorf("duplicate user: %d", code)
	}
	if code, body = h.do("POST", "/v0/users/alice/tokens", "admin", nil); code != 201 || field(body, "token") == "" {
		t.Errorf("token: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/users/bob/tokens", "admin", nil); code != 404 {
		t.Errorf("token for missing user: %d", code)
	}
	if code, _ = h.do("POST", "/v0/users/alice/tokens", "agent", nil); code != 403 {
		t.Errorf("agent creates token: %d", code)
	}
	if code, _ = h.do("POST", "/v0/groups", "admin", map[string]any{"name": "finance"}); code != 201 {
		t.Errorf("create group: %d", code)
	}
	patch := func(add, remove []string) (int, any) {
		req := map[string]any{}
		if add != nil {
			req["add"] = add
		}
		if remove != nil {
			req["remove"] = remove
		}
		return h.do("PATCH", "/v0/groups/finance/members", "admin", req)
	}
	code, body = patch([]string{"alice", "bob"}, nil)
	if code != 200 || len(field(body, "members").([]any)) != 2 {
		t.Errorf("add members: %d %v", code, body)
	}
	// Two admins edit finance from the same view: one removes alice, the
	// other adds carol. Neither undoes the other.
	if code, _ = patch(nil, []string{"alice"}); code != 200 {
		t.Errorf("remove alice: %d", code)
	}
	if code, _ = patch([]string{"carol"}, nil); code != 200 {
		t.Errorf("add carol: %d", code)
	}
	if members := h.server.Identity.(*fakeIdentity).groups["finance"]; !slices.Equal(members, []string{"bob", "carol"}) {
		t.Errorf("finance after both edits: %v", members)
	}
	for _, req := range []any{map[string]any{}, map[string]any{"add": []string{"bob"}, "remove": []string{"bob"}}, map[string]any{"users": []string{}}} {
		if code, _ = h.do("PATCH", "/v0/groups/finance/members", "admin", req); code != 400 {
			t.Errorf("members %v: %d", req, code)
		}
	}
	if code, _ = h.do("PATCH", "/v0/groups/nope/members", "admin", map[string]any{"add": []string{"bob"}}); code != 404 {
		t.Errorf("members of a missing group: %d", code)
	}
	if code, _ = h.do("PATCH", "/v0/groups/finance/members", "viewer", map[string]any{"add": []string{"bob"}}); code != 403 {
		t.Errorf("a member changes a group: %d", code)
	}
	code, body = patch(nil, []string{"bob", "carol"})
	if code != 200 || len(field(body, "members").([]any)) != 0 {
		t.Errorf("empty the group: %d %v", code, body)
	}
	if code, body = h.do("GET", "/v0/groups", "agent", nil); code != 200 || len(body.([]any)) != 2 {
		t.Errorf("groups: %d %v", code, body)
	}
	if code, body = h.do("GET", "/v0/users", "agent", nil); code != 200 || len(body.([]any)) != 1 {
		t.Errorf("users: %d %v", code, body)
	}
}

// --- events -----------------------------------------------------------------

func TestEventStream(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest("GET", h.srv.URL+"/v0/events", nil)
	req.Header.Set("Authorization", "Bearer viewer")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := h.srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if err := h.kube.Create(context.Background(), installed("notes", "oidc")); err != nil {
		t.Fatal(err)
	}
	doc, _ := openapi3.NewLoader().LoadFromData(h.server.OpenAPI)
	schema := doc.Components.Schemas["Event"].Value
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
			var e map[string]any
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatal(err)
			}
			if err := schema.VisitJSON(e); err != nil {
				t.Errorf("event does not match openapi.yaml: %v", err)
			}
			// The app's Ready message ("ok") is for admins and agents (E-63).
			if e["kind"] != "App" || e["name"] != "notes" || !slices.Contains([]any{"ADDED", "MODIFIED"}, e["type"]) || e["message"] != nil {
				t.Errorf("event %v", e)
			}
			return
		}
	}
	t.Fatal("no event received")
}

// --- tokens -----------------------------------------------------------------

func jwt(claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(raw) + ".c2lnbmF0dXJl"
}

// A token counts only if the identity accepts it and it was issued for
// one of the platform's clients (API, console); the answer is cached
// briefly.
func TestTokens(t *testing.T) {
	calls := 0
	accept := map[string]bool{}
	userinfo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !accept[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"preferred_username":"alice","groups":["staff","cell-agents"]}`)
	}))
	defer userinfo.Close()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	tokens := &Tokens{UserinfoURL: userinfo.URL, ClientIDs: []string{"cell-api", "cell-console"}, HTTP: userinfo.Client(), TTL: 30 * time.Second, Now: func() time.Time { return now }}
	exp := float64(now.Add(time.Hour).Unix())
	good := jwt(map[string]any{"aud": "cell-api", "exp": exp})
	otherApp := jwt(map[string]any{"aud": "forgejo", "exp": exp})
	gateway := jwt(map[string]any{"aud": "cell-mcp", "exp": exp})
	console := jwt(map[string]any{"aud": "cell-console", "exp": exp, "sub": "console"})
	revoked := jwt(map[string]any{"aud": []string{"cell-api"}, "exp": exp, "jti": "x"})
	accept[good], accept[otherApp], accept[gateway], accept[console] = true, true, true, true

	c, err := tokens.Check(context.Background(), good)
	if err != nil || c.Username != "alice" || c.Role != RoleAgent {
		t.Fatalf("good token: %+v %v", c, err)
	}
	if _, err := tokens.Check(context.Background(), good); err != nil || calls != 1 {
		t.Errorf("cached check: %v, %d userinfo calls", err, calls)
	}
	now = now.Add(31 * time.Second)
	if _, err := tokens.Check(context.Background(), good); err != nil || calls != 2 {
		t.Errorf("after the TTL: %v, %d userinfo calls", err, calls)
	}
	if _, err := tokens.Check(context.Background(), otherApp); err == nil || !strings.Contains(err.Error(), "not for [cell-api cell-console]") {
		t.Errorf("token of another app: %v", err)
	}
	if _, err := tokens.Check(context.Background(), gateway); err == nil {
		t.Error("token of the MCP gateway accepted")
	}
	if c, err := tokens.Check(context.Background(), console); err != nil || c.Username != "alice" {
		t.Errorf("console token: %+v %v", c, err)
	}
	if _, err := tokens.Check(context.Background(), revoked); err == nil {
		t.Error("token the identity rejects was accepted")
	}
	if _, err := tokens.Check(context.Background(), "not-a-jwt"); err == nil {
		t.Error("garbage accepted")
	}
	if calls != 4 {
		t.Errorf("userinfo calls %d: the audience must be checked first", calls)
	}
}
