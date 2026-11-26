package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// An admin sets the provider; a kept key stays, another provider's goes.
func TestSetModels(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if code, body := h.do("GET", "/v0/models", "agent", nil); code != 200 || field(body, "configured") != false {
		t.Fatalf("before: %d %v", code, body)
	}
	if code, _ := h.do("PUT", "/v0/models", "agent", map[string]any{"apiKey": "k1"}); code != 403 {
		t.Errorf("agent sets: %d", code)
	}
	h.server.CheckModels = func(context.Context, cell.ModelProvider) error { return errors.New("refused the key (401)") }
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"apiKey": "k1"}); code != 400 {
		t.Errorf("failed check: %d", code)
	}
	checks := 0
	h.server.CheckModels = func(context.Context, cell.ModelProvider) error { checks++; return nil }
	if code, body := h.do("PUT", "/v0/models", "admin", map[string]any{"apiKey": "k1"}); code != 200 || field(body, "model") != cell.DefaultModel || field(body, "apiKey") != nil {
		t.Fatalf("set: %d %v", code, body)
	}
	secret := func() corev1.Secret {
		var s corev1.Secret
		if err := h.kube.Get(ctx, client.ObjectKey{Namespace: cell.SystemNamespace, Name: cell.ModelSettings}, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	settings := func() map[string][]byte { return secret().Data }
	if d := settings(); string(d["apiKey"]) != "k1" || string(d["provider"]) != "anthropic" {
		t.Errorf("settings %v", d)
	}
	// What the operator would tell.
	status := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: cell.ModelsStatus}, Data: map[string]string{cell.ModelsStatusKey: `{"configured":true,"provider":"anthropic","model":"claude-opus-5-5"}`, cell.ModelsVersionKey: secret().ResourceVersion}}
	if err := h.kube.Create(ctx, status); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"model": "claude-sonnet-5-5"}); code != 200 || checks != 1 {
		t.Errorf("keep key: %d, %d checks", code, checks)
	}
	if d := settings(); string(d["apiKey"]) != "k1" || string(d["model"]) != "claude-sonnet-5-5" {
		t.Errorf("kept %v", d)
	}
	// The status lags behind: another provider was set since. Its key must
	// not go to the one the status still tells.
	other := secret()
	other.Data = map[string][]byte{"provider": []byte("anthropic"), "baseUrl": []byte("https://other.example.com"), "apiKey": []byte("k2"), "model": []byte("m")}
	if err := h.kube.Update(ctx, &other); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"model": "claude-sonnet-5-5"}); code != 409 {
		t.Errorf("keep on a stale status: %d", code)
	}
	if d := settings(); string(d["baseUrl"]) != "https://other.example.com" || string(d["apiKey"]) != "k2" {
		t.Errorf("stale keep wrote %v", d)
	}
	// The Anthropic address is given without /v1; the gateway adds it.
	if code, body := h.do("PUT", "/v0/models", "admin", map[string]any{"baseUrl": "https://api.anthropic.com/v1/", "apiKey": "k3"}); code != 200 || field(body, "baseUrl") != "https://api.anthropic.com" {
		t.Errorf("anthropic address: %d %v", code, body)
	}
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"baseUrl": "https://elsewhere.example.com"}); code != 400 {
		t.Errorf("kept key to a new address: %d", code)
	}
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"provider": "openai", "model": "qwen3"}); code != 400 {
		t.Errorf("openai without baseUrl: %d", code)
	}
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"provider": "openai", "baseUrl": "http://llm.app-models.svc/v1/", "model": "qwen3"}); code != 200 {
		t.Errorf("openai: %d", code)
	}
	if d := settings(); d["apiKey"] != nil || string(d["baseUrl"]) != "http://llm.app-models.svc/v1" {
		t.Errorf("another provider's key stays: %v", d)
	}
}

// The check lists models where the gateway sends: the Anthropic API under
// /v1, an OpenAI-compatible one under its own address.
func TestCheckModels(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("x-api-key") == "bad" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := checkModels(ctx, cell.ModelProvider{Provider: cell.ProviderAnthropic, BaseURL: srv.URL, APIKey: "k"}); err != nil {
		t.Error(err)
	}
	if err := checkModels(ctx, cell.ModelProvider{Provider: cell.ProviderOpenAI, BaseURL: srv.URL + "/v1"}); err != nil {
		t.Error(err)
	}
	if err := checkModels(ctx, cell.ModelProvider{Provider: cell.ProviderAnthropic, BaseURL: srv.URL, APIKey: "bad"}); err == nil {
		t.Error("a refused key passes")
	}
	if len(paths) != 3 || paths[0] != "/v1/models" || paths[1] != "/v1/models" {
		t.Errorf("paths %v", paths)
	}
}

// Usage sums the gateway's token counts per account; the limit is stored
// and 0 takes it away.
func TestModelsUsageAndLimit(t *testing.T) {
	metrics, err := os.ReadFile("testdata/gateway-metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(metrics) }))
	defer gw.Close()
	h := newHarness(t)
	h.server.ModelsMetrics = gw.URL
	if code, _ := h.do("GET", "/v0/models/usage", "agent", nil); code != 403 {
		t.Errorf("agent reads usage: %d", code)
	}
	code, body := h.do("GET", "/v0/models/usage", "admin", nil)
	want := `[{"account":"app-wiki","inputTokens":40,"outputTokens":7},{"account":"cell-agent","inputTokens":120,"outputTokens":21}]`
	if got, _ := json.Marshal(field(body, "accounts")); code != 200 || string(got) != want {
		t.Errorf("usage: %d %s", code, got)
	}

	h.server.CheckModels = func(context.Context, cell.ModelProvider) error { return nil }
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"apiKey": "k1", "tokensPerHour": -5}); code != 400 {
		t.Errorf("negative limit: %d", code)
	}
	if code, body := h.do("PUT", "/v0/models", "admin", map[string]any{"apiKey": "k1", "tokensPerHour": 50000}); code != 200 || field(body, "tokensPerHour") != float64(50000) {
		t.Errorf("limit: %d %v", code, body)
	}
	var s corev1.Secret
	key := client.ObjectKey{Namespace: cell.SystemNamespace, Name: cell.ModelSettings}
	if err := h.kube.Get(context.Background(), key, &s); err != nil || string(s.Data["tokensPerHour"]) != "50000" {
		t.Fatalf("stored %v %v", s.Data, err)
	}
	if code, _ := h.do("PUT", "/v0/models", "admin", map[string]any{"apiKey": "k2"}); code != 200 {
		t.Errorf("no limit: %d", code)
	}
	if err := h.kube.Get(context.Background(), key, &s); err != nil || s.Data["tokensPerHour"] != nil {
		t.Errorf("limit stays %v %v", s.Data, err)
	}
}

// A cut-off sample is an error, not a crash.
func TestTokenUsageUnreadable(t *testing.T) {
	for _, in := range []string{
		`agentgateway_gen_ai_client_token_usage_sum{account="a",gen_ai_token_type="input"} `,
		`agentgateway_gen_ai_client_token_usage_sum{account="a",gen_ai_token_type="input"`,
		`agentgateway_gen_ai_client_token_usage_sum{account="a",gen_ai_token_type="input"} x`,
	} {
		if _, err := tokenUsage(strings.NewReader(in)); err == nil {
			t.Errorf("no error for %q", in)
		}
	}
}
