package api

import (
	"context"
	"errors"
	"io"
	"net"
	"slices"
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

type fakeDNS map[string][]string

func (f fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) { return f[name], nil }
func (f fakeDNS) LookupAddr(_ context.Context, addr string) ([]string, error) {
	return f["ptr:"+addr], nil
}
func (f fakeDNS) LookupHost(_ context.Context, host string) ([]string, error) {
	return f["host:"+host], nil
}
func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	hosts, ok := f["mx:"+name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	var mxs []*net.MX
	for _, h := range hosts {
		mxs = append(mxs, &net.MX{Host: h})
	}
	return mxs, nil
}

// The delivery check tries each mail server in turn, takes a domain without
// MX records as its own server, and none behind a null MX.
func TestCheckDelivery(t *testing.T) {
	var dialed []string
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		if addr != "up.example:25" {
			return nil, errors.New("blocked")
		}
		conn, peer := net.Pipe()
		go func() { _, _ = peer.Write([]byte("220 up.example ESMTP\r\n")); _, _ = io.Copy(io.Discard, peer) }()
		return conn, nil
	}
	dns := fakeDNS{"mx:a.example": {"down.example.", "up.example."}, "mx:null.example": {"."}}
	for _, c := range []struct {
		domains []string
		ok      bool
		dialed  []string
	}{
		{[]string{"a.example"}, true, []string{"down.example:25", "up.example:25"}},
		{[]string{"up.example"}, true, []string{"up.example:25"}},
		{[]string{"null.example"}, false, nil},
		{[]string{"null.example", "b.example"}, false, []string{"b.example:25"}},
	} {
		dialed = nil
		err := checkDelivery(context.Background(), dns, dial, c.domains)
		if (err == nil) != c.ok || !slices.Equal(dialed, c.dialed) {
			t.Errorf("%v: %v, dialed %v", c.domains, err, dialed)
		}
	}
}

// The cell's own mail server: its DNS records show before it is used, and
// it is taken only once the cell reaches its people's mail servers.
func TestOwnMail(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if code, _ := h.do("PUT", "/v0/mail", "admin", map[string]any{"own": true}); code != 400 {
		t.Errorf("own without a server: %d", code)
	}
	cm := &corev1.ConfigMap{}
	cm.Namespace, cm.Name = "cell-system", cell.OwnMail
	cm.Data = map[string]string{"domain": "example.org", "address": "192.0.2.1", "selector": "mail", "dkim": "KEY"}
	if err := h.server.Kube.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	h.server.DNS = fakeDNS{"example.org": {"v=spf1 ip4:192.0.2.1 -all"}, "mail._domainkey.example.org": {"v=DKIM1; k=rsa; p=OTHER"},
		"ptr:192.0.2.1": {"mail.example.org."}, "host:mail.example.org": {"192.0.2.9"}}
	code, body := h.do("GET", "/v0/mail", "admin", nil)
	own, _ := field(body, "ownServer").(map[string]any)
	if code != 200 || field(body, "own") != false || own == nil || own["from"] != "cell@example.org" {
		t.Fatalf("mail with own server: %d %v", code, body)
	}
	set := map[string]bool{}
	for _, r := range own["records"].([]any) {
		r := r.(map[string]any)
		set[r["name"].(string)] = r["set"].(bool)
	}
	if !set["example.org"] || set["mail._domainkey.example.org"] || set["_dmarc.example.org"] || set["mail.example.org"] || !set["192.0.2.1"] {
		t.Errorf("records: %v", set)
	}

	if code, _ = h.do("PUT", "/v0/mail", "admin", map[string]any{"own": true, "host": "smtp.example.org"}); code != 400 {
		t.Errorf("own with a host: %d", code)
	}
	if code, _ = h.do("PUT", "/v0/mail", "admin", map[string]any{"host": cell.OwnMailHost, "from": "x@example.org"}); code != 400 {
		t.Errorf("own server as relay: %d", code)
	}
	var checked authentik.Mail
	h.server.CheckMail = func(_ context.Context, m authentik.Mail) error { checked = m; return nil }
	if code, _ = h.do("PUT", "/v0/mail", "admin", map[string]any{"own": true}); code != 400 {
		t.Errorf("own with nobody to reach: %d", code)
	}
	h.do("POST", "/v0/users", "admin", map[string]any{"username": "alice", "name": "Alice", "email": "a@Mail.Example.NET"})
	h.do("POST", "/v0/users", "admin", map[string]any{"username": "bob", "name": "Bob", "email": "bob@example.org"})
	var reached []string
	h.server.CheckDelivery = func(_ context.Context, d []string) error { reached = d; return errors.New("port 25 blocked") }
	if code, _ = h.do("PUT", "/v0/mail", "admin", map[string]any{"own": true}); code != 400 || len(reached) != 2 ||
		!slices.Contains(reached, "mail.example.net") || !slices.Contains(reached, "example.org") {
		t.Errorf("blocked port 25: %d %v", code, reached)
	}
	var s corev1.Secret
	if err := h.server.Kube.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-mail"}, &s); err == nil {
		t.Errorf("settings changed after a failed check: %v", s.Data)
	}
	h.server.CheckDelivery = func(context.Context, []string) error { return nil }
	if code, body = h.do("PUT", "/v0/mail", "admin", map[string]any{"own": true}); code != 200 || field(body, "own") != true || !checked.Own() {
		t.Fatalf("own: %d %v %+v", code, body, checked)
	}
	if err := h.server.Kube.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-mail"}, &s); err != nil || string(s.Data["host"]) != cell.OwnMailHost || string(s.Data["from"]) != "cell@example.org" || string(s.Data["username"]) != "" {
		t.Fatalf("own settings: %v %v", err, s.Data)
	}
}
