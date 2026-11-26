package api

import (
	"context"
	"errors"
	"testing"

	"github.com/eigen-hq/cell-platform/internal/authentik"
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
