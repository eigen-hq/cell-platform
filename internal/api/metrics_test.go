package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeStore answers the store's query API: per query the result of the
// answer whose key the query contains, else nothing. It keeps the queries.
type fakeStore struct {
	mu   sync.Mutex
	asks []url.Values
}

func newFakeStore(t *testing.T, answers map[string]string) (*fakeStore, string) {
	t.Helper()
	f := &fakeStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.asks = append(f.asks, q)
		f.mu.Unlock()
		result := "[]"
		for k, v := range answers {
			if strings.Contains(q.Get("query"), k) {
				result = v
			}
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"result":%s}}`, result)
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// A history covers its span in 120 steps; a point keeps what was measured
// then. Machines are open to viewers, apps' usage only to agents.
func TestUsageHistory(t *testing.T) {
	h := newHarness(t, node("z-server", true, true, "203.0.113.1"), installed("notes", "oidc"))
	store, u := newFakeStore(t, map[string]string{
		"_cpu_usage_seconds_total":  `[{"metric":{},"values":[[1795000000,"0.5"],[1795000030,"0.25"],[1795000060,"NaN"]]}]`,
		"_memory_working_set_bytes": `[{"metric":{},"values":[[1795000000,"1048576"]]}]`,
	})
	h.server.Metrics = u
	code, body := h.do("GET", "/v0/nodes/z-server/usage?range=1h", "viewer", nil)
	points, _ := field(body, "points").([]any)
	if code != 200 || field(body, "step") != 30.0 || len(points) != 2 {
		t.Fatalf("node history: %d %v", code, body)
	}
	first, second := points[0].(map[string]any), points[1].(map[string]any)
	if first["cpu"] != 0.5 || first["memory"] != 1048576.0 || second["cpu"] != 0.25 || second["memory"] != nil {
		t.Errorf("points: %v", points)
	}
	// The series are combined before each step is averaged.
	for i, q := range store.asks {
		want := []string{
			`avg_over_time(max(rate(node_cpu_usage_seconds_total{node="z-server"}[1m]))[60s:30s])`,
			`avg_over_time(max(node_memory_working_set_bytes{node="z-server"})[60s:30s])`,
		}
		if !slices.Contains(want, q.Get("query")) || q.Get("step") != "30" {
			t.Errorf("node query %d: %v", i, q)
		}
	}
	store.asks = nil
	if code, _ := h.do("GET", "/v0/apps/notes/usage", "viewer", nil); code != 403 {
		t.Errorf("viewer reads an app's usage: %d", code)
	}
	if code, _ := h.do("GET", "/v0/apps/notes/usage?range=7d", "agent", nil); code != 200 || len(store.asks) != 2 {
		t.Errorf("app history: %d", code)
	}
	for i, q := range store.asks {
		want := []string{
			`avg_over_time(sum(rate(container_cpu_usage_seconds_total{namespace="app-notes"}[1m]))[5040s:60s])`,
			`avg_over_time(sum(container_memory_working_set_bytes{namespace="app-notes"})[5040s:60s])`,
		}
		if !slices.Contains(want, q.Get("query")) {
			t.Errorf("app query %d: %v", i, q)
		}
	}
	for path, want := range map[string]int{
		"/v0/nodes/z-server/usage?range=5m": 400, "/v0/nodes/gone/usage": 404, "/v0/apps/gone/usage": 404,
	} {
		if code, _ := h.do("GET", path, "admin", nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	h.server.Metrics = "http://127.0.0.1:1"
	if code, _ := h.do("GET", "/v0/nodes/z-server/usage", "viewer", nil); code != 503 {
		t.Errorf("store away: %d", code)
	}
}
