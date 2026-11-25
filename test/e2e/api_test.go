//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// apiClient calls the platform API as one account. The tests drive the cell
// through it alone; kubectl only plays the world around the cell (a crashed
// pod, data written by users) and looks inside where the API shows nothing.
type apiClient struct {
	tc                 *testCell
	username, password string
	// task the account's calls belong to (agents work in tasks)
	task string

	mu    sync.Mutex
	token string
	at    time.Time
}

// adminAPI logs in as the identity's admin (akadmin, in cell-admins). Its
// app password comes from the identity directly: that is how a cell's
// first admin gets in. Everything after that goes through the API.
func (tc *testCell) adminAPI(t *testing.T) *apiClient {
	t.Helper()
	var page struct {
		Results []struct {
			PK int `json:"pk"`
		} `json:"results"`
	}
	tc.authentik(t, http.MethodGet, "core/users/?username=akadmin", nil, &page)
	if len(page.Results) != 1 {
		t.Fatalf("akadmin not found: %+v", page)
	}
	id := fmt.Sprintf("e2e-admin-%d", time.Now().UnixNano())
	tc.authentik(t, http.MethodPost, "core/tokens/", map[string]any{
		"identifier": id, "intent": "app_password", "user": page.Results[0].PK,
		"expiring": true, "expires": time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339),
	}, nil)
	var key struct {
		Key string `json:"key"`
	}
	tc.authentik(t, http.MethodGet, "core/tokens/"+id+"/view_key/", nil, &key)
	return &apiClient{tc: tc, username: "akadmin", password: key.Key}
}

// account creates (or reuses) a user through the API and logs in as it.
// The password is for web logins; API logins use an app password.
func (a *apiClient) account(t *testing.T, username, password string, groups ...string) *apiClient {
	t.Helper()
	switch code, body := a.call(t, http.MethodPost, "/v0/users", map[string]any{
		"username": username, "name": "E2E " + username, "email": username + "@example.org", "password": password,
	}); code {
	case http.StatusCreated, http.StatusConflict:
	default:
		t.Fatalf("create user %s: %d %v", username, code, body)
	}
	for _, g := range groups {
		if code, body := a.call(t, http.MethodPost, "/v0/groups", map[string]any{"name": g}); code != http.StatusCreated && code != http.StatusConflict {
			t.Fatalf("create group %s: %d %v", g, code, body)
		}
		a.do(t, http.MethodPatch, "/v0/groups/"+g+"/members", map[string]any{"add": []string{username}}, http.StatusOK)
	}
	token := a.do(t, http.MethodPost, "/v0/users/"+username+"/tokens", nil, http.StatusCreated)
	return &apiClient{tc: a.tc, username: username, password: field(token, "token").(string)}
}

// bearer returns a token, fetching a new one before the old one expires.
func (a *apiClient) bearer(t *testing.T) string {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Since(a.at) < 45*time.Minute {
		return a.token
	}
	a.token, a.at = a.login(t, "cell-api"), time.Now()
	return a.token
}

// login gets a token for one of the identity's clients: cell-api for the
// platform API, cell-mcp for the MCP gateway.
func (a *apiClient) login(t *testing.T, client string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client},
		"username": {a.username}, "password": {a.password}, "scope": {"openid profile"}}
	resp, err := a.tc.akHTTP.PostForm("https://"+a.tc.cfg.Host("auth")+"/application/o/token/", form)
	if err != nil {
		t.Fatal(err)
	}
	data := readBody(t, resp)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(data), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token for %s from %s: HTTP %d %s", a.username, client, resp.StatusCode, data)
	}
	return tok.AccessToken
}

// call sends a request to the API and returns status and decoded body.
func (a *apiClient) call(t *testing.T, method, path string, in any) (int, any) {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "https://"+a.tc.cfg.Host("api")+path, body)
	req.Header.Set("Authorization", "Bearer "+a.bearer(t))
	req.Header.Set("Content-Type", "application/json")
	if a.task != "" {
		req.Header.Set("X-Cell-Task", a.task)
	}
	resp, err := a.tc.akHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data := readBody(t, resp)
	var out any
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		t.Fatalf("%s %s: HTTP %d, not JSON: %.300s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode, out
}

// do is call that fails the test unless the status is want.
func (a *apiClient) do(t *testing.T, method, path string, in any, want int) any {
	t.Helper()
	code, out := a.call(t, method, path, in)
	if code != want {
		t.Fatalf("%s %s as %s: HTTP %d, want %d: %v", method, path, a.username, code, want, out)
	}
	return out
}

func field(v any, path ...string) any {
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	return v
}

// working is the agent at work in a new task that admin asked for.
func (a *apiClient) working(t *testing.T, admin *apiClient, app string) *apiClient {
	t.Helper()
	task := field(admin.do(t, http.MethodPost, "/v0/tasks", map[string]any{"kind": "diagnose", "app": app, "input": "e2e"}, http.StatusCreated), "name").(string)
	a.do(t, http.MethodPost, "/v0/tasks/"+task+"/claim", nil, http.StatusOK)
	return &apiClient{tc: a.tc, username: a.username, password: a.password, task: task}
}

// propose sends a proposal, has admin approve it and returns its name.
func (a *apiClient) propose(t *testing.T, admin *apiClient, method, path string, in any) string {
	t.Helper()
	cr := a.do(t, method, path, in, http.StatusAccepted)
	name := field(cr, "name").(string)
	if field(cr, "approval", "decision") != nil {
		t.Fatalf("change %s recorded with a decision: %v", name, cr)
	}
	admin.do(t, http.MethodPost, "/v0/changes/"+name+"/approve", nil, http.StatusOK)
	return name
}

// waitChange polls a change until it ends; it fails unless it ends in want.
func (a *apiClient) waitChange(t *testing.T, name string, timeout time.Duration, want string) any {
	t.Helper()
	var cr any
	waitFor(t, timeout, func() error {
		cr = a.do(t, http.MethodGet, "/v0/changes/"+name, nil, http.StatusOK)
		switch phase := field(cr, "phase"); phase {
		case want:
			return nil
		case "Succeeded", "Failed", "RolledBack", "Rejected":
			t.Fatalf("change %s ended %s, want %s: %s", name, phase, want, field(cr, "message"))
		}
		return fmt.Errorf("change %s is %v: %v", name, field(cr, "phase"), field(cr, "message"))
	})
	return cr
}

// installed makes sure an app from the catalog runs: installed through an
// approved change if missing, healthy on its current manifest either way.
func (a *apiClient) installed(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	a.installedAs(t, name, map[string]any{"catalog": name}, timeout)
}

// installedAs is installed with the install request to use if it is missing.
func (a *apiClient) installedAs(t *testing.T, name string, install map[string]any, timeout time.Duration) {
	t.Helper()
	if code, _ := a.call(t, http.MethodGet, "/v0/apps/"+name, nil); code == http.StatusNotFound {
		change := a.propose(t, a, http.MethodPost, "/v0/apps", install)
		a.waitChange(t, change, timeout, "Succeeded")
		return
	}
	waitFor(t, timeout, func() error {
		app := a.do(t, http.MethodGet, "/v0/apps/"+name, nil, http.StatusOK)
		for _, c := range field(app, "conditions").([]any) {
			if field(c, "type") == "Ready" && field(c, "status") == "True" && field(app, "runningVersion") == field(app, "version") {
				return nil
			}
		}
		return fmt.Errorf("%s is %v: %v", name, field(app, "phase"), field(app, "conditions"))
	})
}

// backup starts a backup and waits until it completed.
func (a *apiClient) backup(t *testing.T, app string) string {
	t.Helper()
	name := field(a.do(t, http.MethodPost, "/v0/apps/"+app+"/backups", nil, http.StatusCreated), "name").(string)
	waitFor(t, 10*time.Minute, func() error {
		for _, b := range a.do(t, http.MethodGet, "/v0/apps/"+app+"/backups", nil, http.StatusOK).([]any) {
			if field(b, "name") != name {
				continue
			}
			switch field(b, "phase") {
			case "Completed":
				return nil
			case "Failed":
				t.Fatalf("backup %s failed: %v", name, field(b, "message"))
			}
			return fmt.Errorf("backup %s is %v", name, field(b, "phase"))
		}
		return fmt.Errorf("backup %s not listed", name)
	})
	return name
}

// restore brings back a backup through an approved change.
func (a *apiClient) restore(t *testing.T, app, backup string) {
	t.Helper()
	change := a.propose(t, a, http.MethodPost, "/v0/apps/"+app+"/restore", map[string]any{"backup": backup})
	a.waitChange(t, change, 20*time.Minute, "Succeeded")
}

// Approvals. Agents propose, only admins decide, and nobody, not even a
// cluster admin, records a decision past the API. The event stream reports
// the change.
func TestChangeApproval(t *testing.T) {
	tc := newTestCell(t)
	ctx := context.Background()
	admin := tc.adminAPI(t)
	agent := admin.account(t, "e2e-agent", "e2e-Agent-not-secret", "cell-agents")
	if got := field(agent.do(t, http.MethodGet, "/v0/whoami", nil, http.StatusOK), "role"); got != "agent" {
		t.Fatalf("agent role %v", got)
	}
	admin.installed(t, "whoami", 5*time.Minute)

	events, err := tc.stream(t, admin)
	if err != nil {
		t.Fatal(err)
	}
	// Agents work in tasks: the API records each of their calls.
	agent.do(t, http.MethodGet, "/v0/apps/whoami", nil, http.StatusBadRequest)
	agent = agent.working(t, admin, "whoami")
	cr := agent.do(t, http.MethodPatch, "/v0/apps/whoami", map[string]any{"patch": map[string]any{"title": "Who am I"}, "reason": "e2e"}, http.StatusAccepted)
	name := field(cr, "name").(string)
	if field(cr, "requester", "kind") != "Agent" || field(cr, "requester", "name") != "e2e-agent" || field(cr, "task") != agent.task {
		t.Errorf("requester %v, task %v", field(cr, "requester"), field(cr, "task"))
	}
	if steps := calls(admin.do(t, http.MethodGet, "/v0/tasks/"+agent.task, nil, http.StatusOK)); !slices.Equal(steps, []string{"PATCH /v0/apps/whoami"}) {
		t.Errorf("task steps %v", steps)
	}
	agent.do(t, http.MethodPost, "/v0/changes/"+name+"/approve", nil, http.StatusForbidden)
	agent.waitChange(t, name, time.Minute, "WaitingForApproval")
	agent.do(t, http.MethodPost, "/v0/tasks/"+agent.task+"/finish", map[string]any{"result": "proposed " + name}, http.StatusOK)
	if !events(func(e map[string]any) bool { return e["kind"] == "ChangeRequest" && e["name"] == name }) {
		t.Error("no event for the change")
	}

	// Past the API, the cluster refuses decisions, also from a cluster admin.
	var stored cellv1.ChangeRequest
	if err := tc.kube.Get(ctx, client.ObjectKey{Name: name}, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Approval.Decision, stored.Spec.Approval.By = "Approved", "cluster-admin"
	if err := tc.kube.Update(ctx, &stored); err == nil || !strings.Contains(err.Error(), "only the platform API") {
		t.Errorf("cluster admin approved past the API: %v", err)
	}
	preApproved := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-preapproved-"},
		Spec: cellv1.ChangeRequestSpec{App: "whoami", Requester: cellv1.Requester{Kind: "Human", Name: "x"}, Reason: "e2e",
			Action: "remove", Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "x"}},
	}
	if err := tc.kube.Create(ctx, preApproved); err == nil || !apierrors.IsInvalid(err) && !strings.Contains(err.Error(), "always needs approval") {
		t.Errorf("change created with a decision: %v", err)
	}

	admin.do(t, http.MethodPost, "/v0/changes/"+name+"/reject", nil, http.StatusOK)
	done := admin.waitChange(t, name, time.Minute, "Rejected")
	if field(done, "approval", "by") != "akadmin" {
		t.Errorf("decision %v", field(done, "approval"))
	}
}

// stream opens the API's event stream; the returned function reads events
// until one matches (true) or 30 seconds pass (false).
func (tc *testCell) stream(t *testing.T, a *apiClient) (func(func(map[string]any) bool) bool, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+tc.cfg.Host("api")+"/v0/events", nil)
	req.Header.Set("Authorization", "Bearer "+a.bearer(t))
	hc := *tc.akHTTP
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("event stream: HTTP %d", resp.StatusCode)
	}
	lines := make(chan string)
	go func() {
		defer resp.Body.Close()
		s := bufio.NewScanner(resp.Body)
		for s.Scan() {
			select {
			case lines <- s.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return func(match func(map[string]any) bool) bool {
		timeout := time.After(30 * time.Second)
		for {
			select {
			case line := <-lines:
				data, ok := strings.CutPrefix(line, "data: ")
				var e map[string]any
				if ok && json.Unmarshal([]byte(data), &e) == nil && match(e) {
					return true
				}
			case <-timeout:
				return false
			}
		}
	}, nil
}

// Who may use an app is set through the API; others are turned away
// at the login.
func TestAccessGroups(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	const password = "e2e-Passw0rd-not-secret"
	admin.account(t, "e2e-insider", password, "e2e-staff")
	admin.account(t, "e2e-outsider", password)
	admin.installed(t, "whoami", 5*time.Minute)
	setAccess := func(groups []string) {
		change := admin.propose(t, admin, http.MethodPut, "/v0/apps/whoami/access", map[string]any{"groups": groups})
		admin.waitChange(t, change, 5*time.Minute, "Succeeded")
	}
	setAccess([]string{"e2e-staff"})
	t.Cleanup(func() {
		if t.Failed() {
			setAccess([]string{})
		}
	})
	if g := field(admin.do(t, http.MethodGet, "/v0/apps/whoami/access", nil, http.StatusOK), "groups").([]any); len(g) != 1 {
		t.Errorf("access %v", g)
	}

	base := "https://" + tc.cfg.Host("whoami")
	var whoami referenceApp
	for _, ra := range referenceApps {
		if ra.name == "whoami" {
			whoami = ra
		}
	}
	b := tc.browser(t)
	readBody(t, tc.login(t, b, base+"/", "e2e-insider", password))
	if got := whoami.whoAmI(t, b, base); got != "e2e-insider" {
		t.Errorf("member of the group: logged in as %q", got)
	}
	b = tc.browser(t)
	resp, denied := tc.tryLogin(t, b, base+"/", "e2e-outsider", password)
	if resp != nil {
		readBody(t, resp)
	}
	if denied == "" || whoami.whoAmI(t, b, base) == "e2e-outsider" {
		t.Errorf("user outside the group got in (denied: %q)", denied)
	}

	// Open to everyone again: the same user gets in.
	setAccess([]string{})
	b = tc.browser(t)
	readBody(t, tc.login(t, b, base+"/", "e2e-outsider", password))
	if got := whoami.whoAmI(t, b, base); got != "e2e-outsider" {
		t.Errorf("after opening the app: logged in as %q", got)
	}
}
