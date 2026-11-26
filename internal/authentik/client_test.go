package authentik

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI keeps objects per list endpoint and serves list (filtered by one
// query field), create, detail update and delete like authentik does.
type fakeAPI struct {
	mu      sync.Mutex
	objects map[string][]map[string]any // endpoint -> objects
	nextPK  int
	calls   []string
}

func newFakeAPI() *fakeAPI {
	f := &fakeAPI{objects: map[string][]map[string]any{}, nextPK: 100}
	f.objects["flows/instances/"] = []map[string]any{
		{"pk": "flow-authz", "slug": "default-provider-authorization-implicit-consent"},
		{"pk": "flow-inval", "slug": "default-provider-invalidation-flow"},
	}
	f.objects["crypto/certificatekeypairs/"] = []map[string]any{{"pk": "key-1", "name": "authentik Self-signed Certificate"}}
	for _, s := range []string{"openid", "email", "profile"} {
		f.objects["propertymappings/provider/scope/"] = append(f.objects["propertymappings/provider/scope/"],
			map[string]any{"pk": "map-" + s, "managed": "goauthentik.io/providers/oauth2/scope-" + s})
	}
	for _, e := range []string{"core/applications/", "providers/oauth2/", "providers/proxy/"} {
		f.objects[e] = []map[string]any{}
	}
	f.objects["outposts/instances/"] = []map[string]any{{"pk": "outpost-1", "managed": "goauthentik.io/outposts/embedded", "providers": []any{}, "config": map[string]any{"log_level": "info"}}}
	return f
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	f.calls = append(f.calls, r.Method+" "+path)
	for endpoint := range f.objects {
		if path == endpoint {
			switch r.Method {
			case http.MethodGet:
				var out []map[string]any
				for _, o := range f.objects[endpoint] {
					match := true
					for k, v := range r.URL.Query() {
						if k == "page" || k == "page_size" || strings.HasSuffix(k, "__iexact") ||
							endpoint == "core/applications/" && k == "slug" ||
							endpoint == "providers/proxy/" && k == "name" {
							continue // like authentik: these filters are ignored here
						}
						if fmt.Sprint(o[k]) != v[0] {
							match = false
						}
					}
					if match {
						out = append(out, o)
					}
				}
				// Pages of two, like a long list on a real server.
				page := 1
				fmt.Sscan(r.URL.Query().Get("page"), &page)
				start, next := (page-1)*2, 0
				if start > len(out) {
					start = len(out)
				}
				end := start + 2
				if end < len(out) {
					next = page + 1
				} else {
					end = len(out)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"results": out[start:end], "pagination": map[string]any{"next": next}})
			case http.MethodPost:
				var o map[string]any
				_ = json.NewDecoder(r.Body).Decode(&o)
				f.nextPK++
				o["pk"] = f.nextPK
				f.objects[endpoint] = append(f.objects[endpoint], o)
				_ = json.NewEncoder(w).Encode(o)
			}
			return
		}
		if strings.HasPrefix(path, endpoint) {
			id := strings.Trim(strings.TrimPrefix(path, endpoint), "/")
			for i, o := range f.objects[endpoint] {
				// applications are addressed by slug, everything else by pk
				if fmt.Sprint(o["pk"]) == id && endpoint != "core/applications/" || endpoint == "core/applications/" && o["slug"] == id {
					switch r.Method {
					case http.MethodGet:
						_ = json.NewEncoder(w).Encode(o)
					case http.MethodPatch:
						var patch map[string]any
						_ = json.NewDecoder(r.Body).Decode(&patch)
						for k, v := range patch {
							o[k] = v
						}
						_ = json.NewEncoder(w).Encode(o)
					case http.MethodDelete:
						f.objects[endpoint] = append(f.objects[endpoint][:i], f.objects[endpoint][i+1:]...)
						w.WriteHeader(http.StatusNoContent)
					}
					return
				}
			}
			http.Error(w, `{"detail":"Not found."}`, http.StatusNotFound)
			return
		}
	}
	http.Error(w, `{"detail":"unknown endpoint"}`, http.StatusNotFound)
}

func TestEnsureOIDCIsIdempotent(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	o := OIDC{Slug: "forgejo", Title: "Forgejo", ClientID: "forgejo", ClientSecret: "s", RedirectURIs: []string{"https://git.x/cb"}}
	for i := 0; i < 2; i++ {
		if err := c.EnsureOIDC(context.Background(), o); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if n := len(api.objects["providers/oauth2/"]); n != 1 {
		t.Errorf("%d providers, want 1", n)
	}
	api.calls = nil
	if err := c.EnsureOIDC(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, call := range api.calls {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("unchanged client was written: %s", call)
		}
	}
	apps := api.objects["core/applications/"]
	if len(apps) != 1 || fmt.Sprint(apps[0]["provider"]) != fmt.Sprint(api.objects["providers/oauth2/"][0]["pk"]) {
		t.Errorf("applications %v", apps)
	}
	p := api.objects["providers/oauth2/"][0]
	if p["signing_key"] != "key-1" || len(p["property_mappings"].([]any)) != 3 {
		t.Errorf("provider %v", p)
	}
}

func TestProxyLifecycle(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	ctx := context.Background()
	p := Proxy{Slug: "whoami", ExternalHost: "https://whoami.x", AuthentikURL: "https://auth.x"}
	for i := 0; i < 2; i++ {
		if err := c.EnsureProxy(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	api.calls = nil
	if err := c.EnsureProxy(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, call := range api.calls {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("unchanged proxy was written: %s", call)
		}
	}
	outpost := api.objects["outposts/instances/"][0]
	if providers := outpost["providers"].([]any); len(providers) != 1 {
		t.Errorf("outpost providers %v", providers)
	}
	if cfg := outpost["config"].(map[string]any); cfg["authentik_host"] != "https://auth.x" || cfg["log_level"] != "info" {
		t.Errorf("outpost config %v", cfg)
	}
	if err := c.Delete(ctx, "whoami"); err != nil {
		t.Fatal(err)
	}
	if len(api.objects["core/applications/"])+len(api.objects["providers/proxy/"]) != 0 {
		t.Error("objects left after delete")
	}
	if cfg := outpost["config"].(map[string]any); cfg["authentik_host"] != "https://auth.x" || len(outpost["providers"].([]any)) != 0 {
		t.Errorf("outpost after delete %v", outpost)
	}
}

// Regression: authentik ignores ?slug= on the application list. Ensuring a
// second app must not overwrite the first one.
func TestEnsureDoesNotOverwriteOtherApps(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	ctx := context.Background()
	if err := c.EnsureProxy(ctx, Proxy{Slug: "whoami", ExternalHost: "https://whoami.x", AuthentikURL: "https://auth.x"}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureOIDC(ctx, OIDC{Slug: "forgejo", ClientID: "forgejo", ClientSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, a := range api.objects["core/applications/"] {
		slugs[fmt.Sprint(a["slug"])] = true
	}
	if !slugs["whoami"] || !slugs["forgejo"] || len(slugs) != 2 {
		t.Errorf("applications %v", api.objects["core/applications/"])
	}
}

func TestContainsIgnoresOrderAndExtraFields(t *testing.T) {
	have := map[string]any{"pk": 1, "name": "x", "property_mappings": []any{"b", "a"}, "extra": true}
	if !contains(have, map[string]any{"name": "x", "property_mappings": []any{"a", "b"}}) {
		t.Error("reordered list counted as change")
	}
	if contains(have, map[string]any{"name": "y"}) {
		t.Error("changed field not detected")
	}
	if contains(have, map[string]any{"missing": "z"}) {
		t.Error("missing field not detected")
	}
}

// Regression: the proxy provider list ignores the name filter and pages.
// A provider on a later page must be found, not created twice.
func TestFindPagesThroughUnfilteredLists(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	ctx := context.Background()
	for _, slug := range []string{"a", "b", "c", "d"} {
		if err := c.EnsureProxy(ctx, Proxy{Slug: slug, ExternalHost: "https://" + slug + ".x", AuthentikURL: "https://auth.x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.EnsureProxy(ctx, Proxy{Slug: "d", ExternalHost: "https://d.x", AuthentikURL: "https://auth.x"}); err != nil {
		t.Fatal(err)
	}
	if n := len(api.objects["providers/proxy/"]); n != 4 {
		t.Errorf("%d proxy providers, want 4", n)
	}
}

func TestDuplicate(t *testing.T) {
	for body, want := range map[string]bool{
		`{"username":["This field must be unique."]}`:                   true,
		`{"name":["group with this name already exists."]}`:             true,
		`{"username":["Ensure this field has no more than 150 chars"]}`: false,
	} {
		if got := (&Error{Status: 400, Body: body}).Duplicate(); got != want {
			t.Errorf("%s: %v", body, got)
		}
	}
	if (&Error{Status: 500, Body: "already exists"}).Duplicate() {
		t.Error("a server error is no duplicate")
	}
}

// Replacing the groups of an application adds the new restriction before
// the old one goes: a failure halfway never opens the application.
func TestEnsureAccessNeverOpensOnFailure(t *testing.T) {
	api := newFakeAPI()
	api.objects["core/groups/"] = []map[string]any{{"pk": "g-staff", "name": "staff"}, {"pk": "g-finance", "name": "finance"}}
	api.objects["core/applications/"] = []map[string]any{{"pk": "app-1", "slug": "notes"}}
	api.objects["policies/bindings/"] = []map[string]any{{"pk": "b-1", "target": "app-1", "group": "g-staff"}}
	failCreate := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failCreate && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "policies/bindings/") {
			http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	groupsOf := func() []string {
		var out []string
		for _, b := range api.objects["policies/bindings/"] {
			out = append(out, fmt.Sprint(b["group"]))
		}
		return out
	}

	if err := c.EnsureAccess(context.Background(), "notes", []string{"finance"}); err == nil {
		t.Fatal("failed binding not reported")
	}
	if got := groupsOf(); len(got) != 1 || got[0] != "g-staff" {
		t.Errorf("after a failed change the app is restricted to %v, want the old group", got)
	}
	failCreate = false
	if err := c.EnsureAccess(context.Background(), "notes", []string{"finance"}); err != nil {
		t.Fatal(err)
	}
	if got := groupsOf(); len(got) != 1 || got[0] != "g-finance" {
		t.Errorf("bindings %v, want only finance", got)
	}
	if err := c.EnsureAccess(context.Background(), "notes", nil); err != nil || len(groupsOf()) != 0 {
		t.Errorf("opening the app: %v, bindings %v", err, groupsOf())
	}
}

// A public client (the console) has no secret and only the code grant.
func TestEnsureOIDCPublic(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	if err := c.EnsureOIDC(context.Background(), OIDC{Slug: "cell-console", ClientID: "cell-console", RedirectURIs: []string{"https://console.x/"}, Public: true}); err != nil {
		t.Fatal(err)
	}
	p := api.objects["providers/oauth2/"][0]
	if _, ok := p["client_secret"]; ok || p["client_type"] != "public" || fmt.Sprint(p["grant_types"]) != "[authorization_code]" {
		t.Errorf("provider %v", p)
	}
}

// Membership changes go user by user, so nobody else in the group is
// touched by a save.
func TestChangeGroupMembers(t *testing.T) {
	api := newFakeAPI()
	api.objects["core/users/"] = []map[string]any{{"pk": 1, "username": "alice"}, {"pk": 2, "username": "bob"}, {"pk": 3, "username": "carol"}}
	group := map[string]any{"pk": "g-1", "name": "finance", "users_obj": []any{map[string]any{"username": "alice"}, map[string]any{"username": "bob"}}}
	api.objects["core/groups/"] = []map[string]any{group}
	var ops []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if op, ok := strings.CutPrefix(r.URL.Path, "/api/v3/core/groups/g-1/"); ok && r.Method == http.MethodPost {
			var body struct{ PK int }
			_ = json.NewDecoder(r.Body).Decode(&body)
			ops = append(ops, fmt.Sprint(op, body.PK))
			name := map[int]string{1: "alice", 2: "bob", 3: "carol"}[body.PK]
			users := group["users_obj"].([]any)
			kept := []any{}
			for _, u := range users {
				if u.(map[string]any)["username"] != name {
					kept = append(kept, u)
				}
			}
			if op == "add_user/" {
				kept = append(kept, map[string]any{"username": name})
			}
			group["users_obj"] = kept
			w.WriteHeader(http.StatusNoContent)
			return
		}
		q := r.URL.Query()
		q.Del("include_users")
		r.URL.RawQuery = q.Encode()
		api.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	g, err := c.ChangeGroupMembers(context.Background(), "finance", []string{"carol"}, []string{"alice"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(g.Members) != "[bob carol]" {
		t.Errorf("members %v", g.Members)
	}
	if len(ops) != 2 {
		t.Errorf("calls %v, want one per changed user", ops)
	}
	if _, err := c.ChangeGroupMembers(context.Background(), "nope", []string{"carol"}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing group: %v", err)
	}
}

// The gateway's client gets short-lived tokens; a provider made before
// that (a live cell after an installer rerun) is changed to match, and the
// API's client keeps authentik's default.
func TestEnsureAPIClientTokenValidity(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	ctx := context.Background()
	if err := c.EnsureAPIClient(ctx, "cell-api", "Platform API", ""); err != nil {
		t.Fatal(err)
	}
	api.objects["providers/oauth2/"] = append(api.objects["providers/oauth2/"], map[string]any{
		"pk": 1, "name": "cell-mcp", "access_token_validity": "hours=1"})
	if err := c.EnsureAPIClient(ctx, "cell-mcp", "MCP gateway", "minutes=5"); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, p := range api.objects["providers/oauth2/"] {
		got[fmt.Sprint(p["name"])] = p["access_token_validity"]
	}
	if got["cell-mcp"] != "minutes=5" || got["cell-api"] != nil || len(got) != 2 {
		t.Errorf("token validity per provider: %v", got)
	}
}

// authentik makes a token that a caller without superuser rights creates
// its own, whatever user the request names. Such a token is deleted and
// never handed out; one of the named user is.
func TestCreateAppPasswordChecksOwner(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner any // nil: the user the request names
	}{{"named user", nil}, {"caller instead", 7}} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI()
			api.objects["core/users/"] = []map[string]any{{"pk": 7, "username": "platform"}, {"pk": 9, "username": "alice"}}
			tokens := map[string]map[string]any{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rest, ok := strings.CutPrefix(r.URL.Path, "/api/v3/core/tokens/")
				if !ok {
					api.ServeHTTP(w, r)
					return
				}
				id, view := strings.CutSuffix(strings.Trim(rest, "/"), "/view_key")
				switch {
				case r.Method == http.MethodPost && id == "":
					var tok map[string]any
					_ = json.NewDecoder(r.Body).Decode(&tok)
					if tc.owner != nil {
						tok["user"] = tc.owner
					}
					tokens[fmt.Sprint(tok["identifier"])] = tok
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(tok)
				case tokens[id] == nil:
					http.Error(w, `{"detail":"Not found."}`, http.StatusNotFound)
				case r.Method == http.MethodGet && view:
					_ = json.NewEncoder(w).Encode(map[string]string{"key": "secret-" + id})
				case r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(tokens[id])
				case r.Method == http.MethodDelete:
					delete(tokens, id)
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
			key, err := c.CreateAppPassword(context.Background(), "alice")
			if tc.owner == nil {
				if err != nil || !strings.HasPrefix(key, "secret-cell-api-alice-") || len(tokens) != 1 {
					t.Fatalf("key %q, err %v, tokens %v", key, err, tokens)
				}
				return
			}
			if err == nil || key != "" || !strings.Contains(err.Error(), "belongs to user 7") {
				t.Fatalf("key %q, err %v: want an error and no key", key, err)
			}
			if len(tokens) != 0 {
				t.Errorf("tokens left %v, want the wrong one deleted", tokens)
			}
		})
	}
}

// The platform's own account is hidden: not listed, no app password, no
// group change through the API.
func TestPlatformAccountHidden(t *testing.T) {
	api := newFakeAPI()
	api.objects["core/users/"] = []map[string]any{{"pk": 7, "username": PlatformAccount}, {"pk": 9, "username": "alice"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Del("include_users")
		r.URL.RawQuery = q.Encode()
		api.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	ctx := context.Background()
	users, err := c.ListUsers(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "alice" {
		t.Errorf("users %v, err %v", users, err)
	}
	if _, err := c.CreateAppPassword(ctx, PlatformAccount); !errors.Is(err, ErrNotFound) {
		t.Errorf("app password for the platform account: %v", err)
	}
	api.objects["core/groups/"] = []map[string]any{{"pk": "g-1", "name": "finance"}, {"pk": "g-2", "name": PlatformAccount}}
	if _, err := c.ChangeGroupMembers(ctx, "finance", []string{PlatformAccount}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("adding the platform account to a group: %v", err)
	}
	// Its superuser group: not listed, nobody joins it.
	if groups, err := c.ListGroups(ctx); err != nil || len(groups) != 1 || groups[0].Name != "finance" {
		t.Errorf("groups %v, err %v", groups, err)
	}
	if _, err := c.ChangeGroupMembers(ctx, PlatformAccount, []string{"alice"}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("joining the platform's group: %v", err)
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "POST core/tokens/") || strings.HasPrefix(call, "POST core/groups/g-") {
			t.Errorf("call reached authentik: %s", call)
		}
	}
}
