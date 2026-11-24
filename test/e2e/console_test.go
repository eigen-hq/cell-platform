//go:build e2e

package e2e

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The console is served on its own host and logs admins in with its own
// public client (authorization code with PKCE). Its tokens are good for the
// API, the identity lets the console exchange the code from the browser
// (CORS), and accounts outside cell-admins are turned away at the login.
// Last, a real browser signs in (Playwright, console/tests).
func TestConsoleLogin(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	origin := "https://" + tc.cfg.Host("console")
	const password = "console-e2e-Pa55word"
	admin.account(t, "e2e-console-admin", password, "cell-admins")
	admin.account(t, "e2e-console-member", password)

	// The page and its policy.
	resp, err := tc.akHTTP.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	page := readBody(t, resp)
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `<div id="app">`) ||
		!strings.Contains(csp, "connect-src 'self' https://"+tc.cfg.Host("auth")+";") || !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("console page: HTTP %d, CSP %q: %.200s", resp.StatusCode, csp, page)
	}

	// Where to log in, asked on the console's own origin.
	resp, err = tc.akHTTP.Get(origin + "/v0/login")
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		AuthorizationEndpoint, TokenEndpoint, ClientID, RedirectURI, Scope string
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &login); err != nil || login.ClientID != "cell-console" || login.RedirectURI != origin+"/" {
		t.Fatalf("login: %+v %v", login, err)
	}

	authorize := func(t *testing.T) (start, verifier string) {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		verifier = base64.RawURLEncoding.EncodeToString(b)
		sum := sha256.Sum256([]byte(verifier))
		q := url.Values{"response_type": {"code"}, "client_id": {login.ClientID}, "redirect_uri": {login.RedirectURI},
			"scope": {login.Scope}, "state": {"e2e"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
			"code_challenge_method": {"S256"}}
		return login.AuthorizationEndpoint + "?" + q.Encode(), verifier
	}

	t.Run("admin", func(t *testing.T) {
		start, verifier := authorize(t)
		back := tc.login(t, tc.browser(t), start, "e2e-console-admin", password)
		back.Body.Close()
		code := back.Request.URL.Query().Get("code")
		if back.Request.URL.Host != tc.cfg.Host("console") || code == "" || back.Request.URL.Query().Get("state") != "e2e" {
			t.Fatalf("the login came back to %s", back.Request.URL)
		}
		// The exchange as the browser sends it: from the console's origin.
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {login.RedirectURI},
			"client_id": {login.ClientID}, "code_verifier": {verifier}}
		req, _ := http.NewRequest(http.MethodPost, login.TokenEndpoint, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		resp, err := tc.akHTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data := readBody(t, resp)
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin && got != "*" {
			t.Errorf("token endpoint allows origin %q, want %s", got, origin)
		}
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal([]byte(data), &tok); err != nil || tok.AccessToken == "" {
			t.Fatalf("token: HTTP %d %s", resp.StatusCode, data)
		}
		// The console's token works for the API, with the admin's role.
		req, _ = http.NewRequest(http.MethodGet, origin+"/v0/whoami", nil)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		resp, err = tc.akHTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var me struct{ Username, Role string }
		if body := readBody(t, resp); json.Unmarshal([]byte(body), &me) != nil || me.Username != "e2e-console-admin" || me.Role != "admin" {
			t.Fatalf("whoami with the console's token: HTTP %d %s", resp.StatusCode, body)
		}
	})

	t.Run("member is refused", func(t *testing.T) {
		start, _ := authorize(t)
		if _, denied := tc.tryLogin(t, tc.browser(t), start, "e2e-console-member", password); denied == "" {
			t.Fatal("an account outside cell-admins got into the console")
		}
	})

	t.Run("browser", func(t *testing.T) {
		if os.Getenv("SKIP_BROWSER") != "" {
			t.Skip("SKIP_BROWSER is set")
		}
		cmd := exec.Command("npx", "playwright", "test")
		cmd.Dir = filepath.Join(repoRoot(), "console")
		cmd.Env = append(os.Environ(), "CONSOLE_URL="+origin, "CONSOLE_ADMIN_USER=e2e-console-admin", "CONSOLE_ADMIN_PASSWORD="+password)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("playwright: %v\n%s", err, out)
		}
	})
}
