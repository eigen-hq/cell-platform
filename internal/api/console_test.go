package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLogin(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.do("GET", "/v0/login", "", nil); code != http.StatusNotFound {
		t.Errorf("without a login: %d", code)
	}
	h.server.Login = &Login{Issuer: "https://auth.x/application/o/cell-console/", AuthorizationEndpoint: "https://auth.x/application/o/authorize/",
		TokenEndpoint: "https://auth.x/application/o/token/", EndSessionEndpoint: "https://auth.x/application/o/cell-console/end-session/",
		ClientID: "cell-console", RedirectURI: "https://console.x/", Scope: "openid profile email"}
	if code, body := h.do("GET", "/v0/login", "", nil); code != http.StatusOK || field(body, "clientId") != "cell-console" {
		t.Errorf("login: %d %v", code, body)
	}
}

// The console host serves the console's files, but every /v0/ path stays
// the API's; the API host never serves the console.
func TestConsole(t *testing.T) {
	h := newHarness(t)
	h.server.Console = &Console{Host: "console.x", AuthOrigin: "https://auth.x", Files: fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html>console")},
		"assets/app-1a2b.js": {Data: []byte("app()")},
	}}
	get := func(host, path, token string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
		req.Host = host
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		return resp, string(data)
	}
	for _, path := range []string{"/", "/apps/notes"} {
		resp, body := get("console.x:443", path, "")
		if resp.StatusCode != 200 || body != "<!doctype html>console" || resp.Header.Get("Cache-Control") != "no-cache" ||
			!strings.Contains(resp.Header.Get("Content-Security-Policy"), "connect-src 'self' https://auth.x;") {
			t.Errorf("console %s: %d %q %v", path, resp.StatusCode, body, resp.Header)
		}
	}
	if resp, body := get("console.x", "/assets/app-1a2b.js", ""); body != "app()" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %q %v", body, resp.Header)
	}
	if resp, _ := get("console.x", "/assets/app-old.js", ""); resp.StatusCode != 404 {
		t.Errorf("missing asset: %d", resp.StatusCode)
	}
	if _, body := get("console.x", "/v0/whoami", "admin"); !strings.Contains(body, `"username":"admin-user"`) {
		t.Errorf("API on the console host: %q", body)
	}
	if resp, body := get("console.x", "/v0/nothing", "admin"); resp.StatusCode != 404 || !strings.Contains(body, "no such endpoint") {
		t.Errorf("unknown API path on the console host: %d %q", resp.StatusCode, body)
	}
	if resp, body := get("api.x", "/", ""); resp.StatusCode != 404 || strings.Contains(body, "console") {
		t.Errorf("API host: %d %q", resp.StatusCode, body)
	}
}
