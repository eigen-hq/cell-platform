//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// testCell bundles what the e2e tests need to talk to the local cell.
type testCell struct {
	kube   client.Client
	cfg    cell.Config
	ca     []byte
	akHTTP *http.Client
	token  string
}

func newTestCell(t *testing.T) *testCell {
	t.Helper()
	ctx := context.Background()
	c := kube(t)
	cfg, err := cell.LoadConfig(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := cell.LoadCA(ctx, c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	hc, err := cell.HTTPClient(cfg.Domain, ca)
	if err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: cell.SystemNamespace, Name: "identity"}, &s); err != nil {
		t.Fatal(err)
	}
	return &testCell{kube: c, cfg: cfg, ca: ca, akHTTP: hc, token: string(s.Data["AUTHENTIK_BOOTSTRAP_TOKEN"])}
}

// authentik calls the authentik API as admin.
func (tc *testCell) authentik(t *testing.T, method, path string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "https://"+tc.cfg.Host("auth")+"/api/v3/"+path, body)
	req.Header.Set("Authorization", "Bearer "+tc.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := tc.akHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("authentik %s %s: %d %s", method, path, resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// user creates (or reuses) a person in the identity and sets its password.
func (tc *testCell) user(t *testing.T, username, password string) {
	t.Helper()
	var page struct {
		Results []struct {
			PK int `json:"pk"`
		} `json:"results"`
	}
	tc.authentik(t, http.MethodGet, "core/users/?username="+url.QueryEscape(username), nil, &page)
	var pk int
	if len(page.Results) > 0 {
		pk = page.Results[0].PK
	} else {
		var u struct {
			PK int `json:"pk"`
		}
		tc.authentik(t, http.MethodPost, "core/users/", map[string]any{
			"username": username, "name": "E2E " + username, "email": username + "@example.org", "is_active": true,
		}, &u)
		pk = u.PK
	}
	tc.authentik(t, http.MethodPost, fmt.Sprintf("core/users/%d/set_password/", pk), map[string]any{"password": password}, nil)
}

// browser is an HTTP client with cookies that follows redirects, like a
// browser without JavaScript.
func (tc *testCell) browser(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc, err := cell.HTTPClient(tc.cfg.Domain, tc.ca)
	if err != nil {
		t.Fatal(err)
	}
	hc.Jar = jar
	hc.Timeout = 60 * time.Second
	return hc
}

// login opens start in the browser. When the app sends it to the identity's
// login flow, it answers the flow's stages the way the login page's
// JavaScript would and follows the final redirect back into the app.
// It returns the response the app finally serves.
func (tc *testCell) login(t *testing.T, b *http.Client, start, username, password string) *http.Response {
	t.Helper()
	resp, denied := tc.tryLogin(t, b, start, username, password)
	if denied != "" {
		t.Fatalf("the identity turned %s away: %s", username, denied)
	}
	return resp
}

// tryLogin is login that returns the identity's reason instead of the
// app's response when the identity turns the user away.
func (tc *testCell) tryLogin(t *testing.T, b *http.Client, start, username, password string) (*http.Response, string) {
	t.Helper()
	resp, err := b.Get(start)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	u := resp.Request.URL
	if u.Host != tc.cfg.Host("auth") || !strings.HasPrefix(u.Path, "/if/flow/") {
		t.Fatalf("%s did not lead to the login page but to %s (HTTP %d)", start, u, resp.StatusCode)
	}
	// Login, then the app's authorization flow, which may deny access.
	for flow := 0; flow < 3; flow++ {
		slug := strings.Trim(strings.TrimPrefix(u.Path, "/if/flow/"), "/")
		executor := "https://" + u.Host + "/api/v3/flows/executor/" + slug + "/?query=" + url.QueryEscape(u.RawQuery)
		var answer map[string]any
		for step := 0; ; step++ {
			if step == 10 {
				t.Fatal("login flow did not finish")
			}
			var ch map[string]any
			if answer == nil {
				ch = tc.flowCall(t, b, http.MethodGet, executor, nil)
			} else {
				ch = tc.flowCall(t, b, http.MethodPost, executor, answer)
			}
			switch ch["component"] {
			case "ak-stage-identification":
				answer = map[string]any{"component": ch["component"], "uid_field": username}
				if ch["password_fields"] == true {
					answer["password"] = password
				}
				continue
			case "ak-stage-password":
				answer = map[string]any{"component": ch["component"], "password": password}
				continue
			case "ak-stage-access-denied":
				return nil, fmt.Sprint(ch["error_message"])
			case "xak-flow-redirect":
				to, _ := url.Parse(fmt.Sprint(ch["to"]))
				resp, err = b.Get(u.ResolveReference(to).String())
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatalf("unexpected login stage: %v", ch)
			}
			break
		}
		next := resp.Request.URL
		if next.Host != tc.cfg.Host("auth") {
			return resp, ""
		}
		if !strings.HasPrefix(next.Path, "/if/flow/") {
			// The identity answers itself, e.g. its access denied page.
			page := readBody(t, resp)
			if i := strings.Index(strings.ToLower(page), "denied"); i >= 0 {
				return nil, "the identity denied access at " + next.Path
			}
			t.Fatalf("login ended at the identity's %s: %.300s", next.Path, page)
		}
		resp.Body.Close()
		u = next
	}
	t.Fatal("login went through too many flows")
	return nil, ""
}

func (tc *testCell) flowCall(t *testing.T, b *http.Client, method, u string, body any) map[string]any {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, u, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	parsed, _ := url.Parse(u)
	for _, c := range b.Jar.Cookies(parsed) {
		if c.Name == "authentik_csrf" {
			req.Header.Set("X-authentik-CSRF", c.Value)
		}
	}
	resp, err := b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("flow %s %s: HTTP %d: %s", method, u, resp.StatusCode, data)
	}
	return out
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
