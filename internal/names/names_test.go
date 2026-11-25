package names

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDNS map[string][]string

// slowDNS holds the answer of the first read of a record until release is
// closed: the reader goes on with what it read.
type slowDNS struct {
	fakeDNS
	hold    string
	release chan struct{}
	once    sync.Once
}

func (d *slowDNS) Get(ctx context.Context, name, typ string) ([]string, error) {
	v, err := d.fakeDNS.Get(ctx, name, typ)
	if name+" "+typ == d.hold {
		d.once.Do(func() { <-d.release })
	}
	return v, err
}

var owners = map[string]string{}

func (f fakeDNS) Get(_ context.Context, name, typ string) ([]string, error) { return f[name+" "+typ], nil }
func (f fakeDNS) Set(_ context.Context, cell, name, typ string, v []string, _ int) error {
	f[name+" "+typ] = slices.Clone(v)
	owners[name+" "+typ] = cell
	return nil
}
func (f fakeDNS) Owned(_ context.Context, cell, typ string) ([]string, error) {
	var out []string
	for k, c := range owners {
		if n, t, _ := strings.Cut(k, " "); c == cell && t == typ && f[k] != nil {
			out = append(out, n)
		}
	}
	return out, nil
}

func call(t *testing.T, h http.Handler, method, path string, header map[string]string, body any) (int, map[string]string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]string{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestCells(t *testing.T) {
	dns := fakeDNS{}
	s := &Server{Zone: "cells.test", Keys: []string{"reg-key"}, Secret: []byte("s"), DNS: dns}
	h := s.Handler()
	reg := func(key, name, addr string) (int, map[string]string) {
		return call(t, h, "POST", "/v0/cells", map[string]string{"Authorization": "Bearer " + key}, map[string]string{"name": name, "address": addr})
	}
	if code, _ := reg("wrong", "acme", "10.0.0.5"); code != 401 {
		t.Errorf("without key: %d", code)
	}
	for _, bad := range [][2]string{{"Acme", "10.0.0.5"}, {"a.b", "10.0.0.5"}, {"acme", "127.0.0.1"}, {"acme", "::1"}, {"acme", "x"}} {
		if code, _ := reg("reg-key", bad[0], bad[1]); code != 400 {
			t.Errorf("register %v: %d", bad, code)
		}
	}
	code, a := reg("reg-key", "acme", "10.0.0.5")
	if code != 201 || !labelRe.MatchString(a["label"]) || !strings.HasPrefix(a["label"], "acme-") || a["domain"] != a["label"]+".cells.test" || a["token"] != s.Token(a["label"]) {
		t.Fatalf("register: %d %v", code, a)
	}
	if !slices.Equal(dns[a["label"]+" A"], []string{"10.0.0.5"}) || !slices.Equal(dns["*."+a["label"]+" A"], []string{"10.0.0.5"}) {
		t.Errorf("records %v", dns)
	}
	_, b := reg("reg-key", "acme", "10.0.0.6")

	// A cell moves and answers challenges with its own token only.
	auth := func(label, token string) map[string]string { return map[string]string{"Authorization": "Bearer " + token} }
	if code, _ := call(t, h, "PUT", "/v0/cells/"+a["label"], auth(a["label"], b["token"]), map[string]string{"address": "10.0.0.7"}); code != 401 {
		t.Errorf("move with another cell's token: %d", code)
	}
	if code, _ := call(t, h, "PUT", "/v0/cells/"+a["label"], auth(a["label"], a["token"]), map[string]string{"address": "10.0.0.7"}); code != 200 || dns["*."+a["label"]+" A"][0] != "10.0.0.7" {
		t.Errorf("move: %d %v", code, dns)
	}
	acme := func(label, token, sub, txt string) int {
		code, _ := call(t, h, "POST", "/update", map[string]string{"X-Api-User": label, "X-Api-Key": token}, map[string]string{"subdomain": sub, "txt": txt})
		return code
	}
	digest := func(c string) string { return strings.Repeat(c, 43) }
	if code := acme(a["label"], b["token"], "forgejo", digest("a")); code != 401 {
		t.Errorf("challenge with another cell's token: %d", code)
	}
	for _, sub := range []string{"../x", "a/b", "A", "a..b", "a.b", "-a"} {
		if code := acme(a["label"], a["token"], sub, digest("a")); code != 400 {
			t.Errorf("challenge for %q: %d", sub, code)
		}
	}
	if code := acme(a["label"], a["token"], "forgejo", "short"); code != 400 {
		t.Errorf("challenge with a bad digest: %d", code)
	}
	for _, c := range []string{"a", "b", "c"} {
		if code := acme(a["label"], a["token"], "forgejo", digest(c)); code != 200 {
			t.Errorf("challenge %s: %d", c, code)
		}
	}
	if got := dns["_acme-challenge.forgejo."+a["label"]+" TXT"]; !slices.Equal(got, []string{`"` + digest("c") + `"`}) {
		t.Errorf("the newest challenge replaces the one before: %v", got)
	}
	if code := acme(a["label"], a["token"], "forgejo", digest("c")); code != 200 {
		t.Errorf("repeated challenge: %d", code)
	}
	if got := dns["_acme-challenge.forgejo."+a["label"]+" TXT"]; !slices.Equal(got, []string{`"` + digest("c") + `"`}) {
		t.Errorf("a repeated challenge keeps one value: %v", got)
	}
	// The challenge makes forgejo.<label> a node the wildcard no longer
	// answers (RFC 4592): it has its own address, also after a move.
	if got := dns["forgejo."+a["label"]+" A"]; !slices.Equal(got, []string{"10.0.0.7"}) {
		t.Errorf("challenged host keeps its address: %v", got)
	}
	if code, _ := call(t, h, "PUT", "/v0/cells/"+a["label"], auth(a["label"], a["token"]), map[string]string{"address": "10.0.0.8"}); code != 200 {
		t.Fatalf("second move: %d", code)
	}
	for _, n := range []string{a["label"], "*." + a["label"], "forgejo." + a["label"]} {
		if got := dns[n+" A"]; !slices.Equal(got, []string{"10.0.0.8"}) {
			t.Errorf("%s after the move: %v", n, got)
		}
	}
	if got := dns[b["label"]+" A"]; !slices.Equal(got, []string{"10.0.0.6"}) {
		t.Errorf("the other cell stays: %v", got)
	}
	if code := acme(a["label"], a["token"], "", digest("d")); code != 200 || dns["_acme-challenge."+a["label"]+" TXT"] == nil {
		t.Errorf("challenge for the cell domain: %d", code)
	}
}

func TestHetzner(t *testing.T) {
	var calls []string
	polls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.EscapedPath()+" "+string(body))
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/zones/cells.test/rrsets/a/A" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"rrset":{"records":[{"value":"10.0.0.5"}]}}`)
		case strings.HasPrefix(r.URL.Path, "/zones/cells.test/rrsets/new/"), r.URL.Path == "/zones/cells.test/rrsets/a/TXT":
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found"}}`)
		case r.URL.Path == "/zones/cells.test/rrsets" && r.Method == "POST":
			_, _ = io.WriteString(w, `{"rrset":{},"action":{"id":7,"status":"running"}}`)
		case r.URL.Path == "/actions/7":
			polls++
			if polls < 2 {
				_, _ = io.WriteString(w, `{"action":{"id":7,"status":"running"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"action":{"id":7,"status":"success"}}`)
		case r.URL.Path == "/zones/cells.test/rrsets/bad/A/actions/set_records":
			_, _ = io.WriteString(w, `{"action":{"id":8,"status":"error","error":{"code":"invalid","message":"no"}}}`)
		case r.URL.Path == "/zones/cells.test/rrsets" && r.URL.Query().Get("page") == "1":
			_, _ = io.WriteString(w, `{"rrsets":[{"name":"a"},{"name":"*.a"}],"meta":{"pagination":{"next_page":2}}}`)
		case r.URL.Path == "/zones/cells.test/rrsets" && r.URL.Query().Get("page") == "2":
			_, _ = io.WriteString(w, `{"rrsets":[{"name":"forgejo.a"}],"meta":{"pagination":{"next_page":null}}}`)
		default:
			_, _ = io.WriteString(w, `{"action":{"id":1,"status":"success"}}`)
		}
	}))
	defer api.Close()
	h := &Hetzner{BaseURL: api.URL, Token: "tok", Zone: "cells.test", Poll: time.Millisecond}
	ctx := context.Background()
	if v, err := h.Get(ctx, "a", "A"); err != nil || !slices.Equal(v, []string{"10.0.0.5"}) {
		t.Errorf("get: %v %v", v, err)
	}
	if v, err := h.Get(ctx, "a", "TXT"); err != nil || v != nil {
		t.Errorf("get missing: %v %v", v, err)
	}
	calls = nil
	if err := h.Set(ctx, "a", "new", "A", []string{"10.0.0.9"}, 300); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || !strings.HasPrefix(calls[0], "POST /zones/cells.test/rrsets/new/A/actions/set_records ") ||
		calls[1] != `POST /zones/cells.test/rrsets {"labels":{"cell":"a"},"name":"new","records":[{"value":"10.0.0.9"}],"ttl":300,"type":"A"}` ||
		!strings.HasPrefix(calls[3], "GET /actions/7 ") {
		t.Errorf("set creates a missing record set for the cell and waits for it: %v", calls)
	}
	if err := h.Set(ctx, "a", "bad", "A", []string{"1.2.3.4"}, 300); err == nil || !strings.Contains(err.Error(), "invalid: no") {
		t.Errorf("a failed change must fail: %v", err)
	}
	if err := (&Hetzner{BaseURL: api.URL, Token: "x", Zone: "cells.test"}).Set(ctx, "a", "a", "A", []string{"1.2.3.4"}, 300); err == nil {
		t.Error("a refused token must fail")
	}
	if names, err := h.Owned(ctx, "a", "A"); err != nil || !slices.Equal(names, []string{"a", "*.a", "forgejo.a"}) {
		t.Errorf("owned over two pages: %v %v", names, err)
	}
}

// A move while a challenge reads the cell's address must win: the host ends
// on the new address.
func TestMoveDuringChallenge(t *testing.T) {
	dns := &slowDNS{fakeDNS: fakeDNS{}, release: make(chan struct{})}
	s := &Server{Zone: "cells.test", Keys: []string{"k"}, Secret: []byte("s"), DNS: dns}
	h := s.Handler()
	_, a := call(t, h, "POST", "/v0/cells", map[string]string{"Authorization": "Bearer k"}, map[string]string{"name": "acme", "address": "10.0.0.5"})
	label := a["label"]
	dns.hold = label + " A"
	challenged := make(chan int)
	go func() {
		code, _ := call(t, h, "POST", "/update", map[string]string{"X-Api-User": label, "X-Api-Key": a["token"]},
			map[string]string{"subdomain": "forgejo", "txt": strings.Repeat("a", 43)})
		challenged <- code
	}()
	moved := make(chan int)
	go func() {
		time.Sleep(20 * time.Millisecond) // the challenge holds its read
		code, _ := call(t, h, "PUT", "/v0/cells/"+label, map[string]string{"Authorization": "Bearer " + a["token"]}, map[string]string{"address": "10.0.0.9"})
		moved <- code
	}()
	time.Sleep(60 * time.Millisecond) // the move ran, or waits for the challenge
	close(dns.release)
	if c, m := <-challenged, <-moved; c != 200 || m != 200 {
		t.Fatalf("challenge %d, move %d", c, m)
	}
	if got := dns.fakeDNS["forgejo."+label+" A"]; !slices.Equal(got, []string{"10.0.0.9"}) {
		t.Errorf("host after the race: %v", got)
	}
}
