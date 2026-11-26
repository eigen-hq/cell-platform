package api

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/authentik"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// Mail is taken only after its check passes; an invite mails the link once
// mail is set and returns it before.
func TestMailAndInvites(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v0/users", "admin", map[string]any{"username": "alice", "name": "Alice", "email": "a@example.org"})
	code, body := h.do("POST", "/v0/users/alice/invite", "admin", nil)
	if code != 201 || field(body, "mailed") != false || field(body, "link") == nil {
		t.Fatalf("invite without mail: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/users/alice/invite", "viewer", nil); code != 403 {
		t.Errorf("viewer invites: %d", code)
	}
	if code, body = h.do("GET", "/v0/mail", "admin", nil); code != 200 || field(body, "configured") != false {
		t.Errorf("mail before: %d %v", code, body)
	}
	smtp := map[string]any{"host": "smtp.example.org", "username": "cell", "password": "pw", "from": "cell@example.org"}
	h.server.CheckMail = func(context.Context, authentik.Mail) error { return errors.New("connection refused") }
	if code, _ = h.do("PUT", "/v0/mail", "admin", smtp); code != 400 {
		t.Errorf("failed check: %d", code)
	}
	h.server.CheckMail = func(context.Context, authentik.Mail) error { return nil }
	if code, _ = h.do("PUT", "/v0/mail", "admin", map[string]any{"host": "smtp.example.org", "from": "Cell <cell@example.org>"}); code != 400 {
		t.Errorf("display name in from: %d", code)
	}
	if code, body = h.do("PUT", "/v0/mail", "admin", smtp); code != 200 || field(body, "port") != float64(587) {
		t.Fatalf("set mail: %d %v", code, body)
	}
	// The API writes only Secret cell-mail; the operator sets the identity
	// from it (TestMailSync).
	var s corev1.Secret
	if err := h.server.Kube.Get(context.Background(), client.ObjectKey{Namespace: "cell-system", Name: "cell-mail"}, &s); err != nil || string(s.Data["password"]) != "pw" || string(s.Data["port"]) != "587" {
		t.Fatalf("mail settings for the operator: %v %v", err, s.Data)
	}
	// An empty password keeps the one set before, but only for the same
	// server and login.
	if code, body = h.do("PUT", "/v0/mail", "admin", map[string]any{"host": "smtp.example.org", "username": "cell", "from": "it@example.org"}); code != 200 {
		t.Fatalf("change sender only: %d %v", code, body)
	}
	if err := h.server.Kube.Get(context.Background(), client.ObjectKey{Namespace: "cell-system", Name: "cell-mail"}, &s); err != nil || string(s.Data["password"]) != "pw" || string(s.Data["from"]) != "it@example.org" {
		t.Fatalf("password kept: %v %v", err, s.Data)
	}
	if code, _ = h.do("PUT", "/v0/mail", "admin", map[string]any{"host": "smtp.other.example", "username": "cell", "from": "it@example.org"}); code != 400 {
		t.Errorf("password to another server: %d", code)
	}
	m, err := cell.ReadMail(s.Data)
	if err != nil {
		t.Fatal(err)
	}
	h.server.Identity.(*fakeIdentity).setMail(m)
	if code, body = h.do("GET", "/v0/mail", "admin", nil); code != 200 || field(body, "configured") != true || field(body, "password") != nil {
		t.Errorf("mail after: %d %v", code, body)
	}
	if code, body = h.do("POST", "/v0/users/alice/invite", "admin", nil); code != 201 || field(body, "mailed") != true || field(body, "link") != nil {
		t.Errorf("invite with mail: %d %v", code, body)
	}
	if code, _ = h.do("POST", "/v0/users/bob/invite", "admin", nil); code != 404 {
		t.Errorf("invite unknown: %d", code)
	}
}

// Admins remove people, but not themselves, superusers or service accounts.
func TestRemoveUser(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v0/users", "admin", map[string]any{"username": "alice", "name": "Alice"})
	h.do("POST", "/v0/users", "admin", map[string]any{"username": "admin-user", "name": "Me"})
	for _, c := range []struct {
		name, caller string
		want         int
	}{
		{"alice", "viewer", 403},
		{"admin-user", "admin", 400},
		{"akadmin", "admin", 403},
		{"cell-agent-ops", "admin", 400},
		{"bob", "admin", 404},
		{"alice", "admin", 204},
		{"alice", "admin", 404},
	} {
		if code, body := h.do("DELETE", "/v0/users/"+c.name, c.caller, nil); code != c.want {
			t.Errorf("%s removes %s: %d %v, want %d", c.caller, c.name, code, body, c.want)
		}
	}
}
