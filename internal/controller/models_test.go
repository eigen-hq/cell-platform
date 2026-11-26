package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// The gateway admits only the agent's key, by its hash, and sends every
// model name to the configured model; settings from before the gateway are
// Anthropic.
func TestModelsConfig(t *testing.T) {
	ctx := context.Background()
	agentKey := strings.Repeat("a", 64)
	providerKey := "provider-" + strings.Repeat("x", 8)
	c := fakeClient(k8s.Scheme(),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-agent"}, Data: map[string][]byte{"modelKey": []byte(agentKey)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-agent-llm"}, Data: map[string][]byte{"apiKey": []byte(providerKey)}})
	r := &ModelsReconciler{Client: c}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-models"}, &s); err != nil {
		t.Fatal(err)
	}
	var conf struct {
		LLM struct {
			Models []struct {
				Name, Provider string
				Params         map[string]string
			}
			Policies struct {
				APIKey struct {
					Mode string
					Keys []struct {
						KeyHash string `json:"keyHash"`
					}
				} `json:"apiKey"`
			}
		} `json:"llm"`
	}
	if err := yaml.Unmarshal(s.Data["config.yaml"], &conf); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(agentKey))
	keys := conf.LLM.Policies.APIKey.Keys
	if conf.LLM.Policies.APIKey.Mode != "strict" || len(keys) != 1 || keys[0].KeyHash != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("api key policy %+v", conf.LLM.Policies.APIKey)
	}
	if strings.Contains(string(s.Data["config.yaml"]), agentKey) {
		t.Error("the agent's key is in the gateway's configuration")
	}
	m := conf.LLM.Models
	if len(m) != 1 || m[0].Name != "*" || m[0].Provider != "anthropic" || m[0].Params["model"] != cell.DefaultModel || m[0].Params["apiKey"] != providerKey {
		t.Errorf("models %+v", m)
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-models-status"}, &cm); err != nil {
		t.Fatal(err)
	}
	if got := cm.Data["models.json"]; got != `{"configured":true,"provider":"anthropic","model":"claude-opus-5-5"}` || strings.Contains(got, providerKey) {
		t.Errorf("status %s", got)
	}
	set := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-agent-llm"}, set); err != nil {
		t.Fatal(err)
	}
	if cm.Data["settingsVersion"] != set.ResourceVersion {
		t.Errorf("status of version %q, settings %q", cm.Data["settingsVersion"], set.ResourceVersion)
	}
	render := func(data map[string][]byte) map[string]string {
		t.Helper()
		set.Data = data
		if err := c.Update(ctx, set); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(&s), &s); err != nil {
			t.Fatal(err)
		}
		conf.LLM.Models = nil
		if err := yaml.Unmarshal(s.Data["config.yaml"], &conf); err != nil {
			t.Fatal(err)
		}
		if len(conf.LLM.Models) != 1 {
			t.Fatalf("models %+v", conf.LLM.Models)
		}
		return conf.LLM.Models[0].Params
	}
	// The gateway takes baseUrl as the whole prefix and adds /messages: the
	// Anthropic address gets the API's /v1, the one the check lists models at.
	if p := render(map[string][]byte{"baseUrl": []byte("https://anthropic.example.com"), "apiKey": []byte(providerKey)}); p["baseUrl"] != "https://anthropic.example.com/v1" {
		t.Errorf("anthropic params %+v", p)
	}

	// An own model behind an OpenAI-compatible address needs no key; its
	// address already is the whole prefix.
	p := render(map[string][]byte{"provider": []byte("openai"), "baseUrl": []byte("http://llm.app-models.svc/v1"), "model": []byte("qwen3")})
	if conf.LLM.Models[0].Provider != "openAI" || p["baseUrl"] != "http://llm.app-models.svc/v1" || p["apiKey"] != "" {
		t.Errorf("models %+v", conf.LLM.Models)
	}

	// An app that needs models gets in with its own key, once its reconcile
	// made it; each account has its own token bucket.
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "wiki"}, Spec: cellv1.AppSpec{Needs: cellv1.Needs{Models: true}}}
	if err := c.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	res, err := r.Reconcile(ctx, ctrl.Request{})
	if err != nil || res.RequeueAfter == 0 {
		t.Errorf("without the app's key: %+v %v", res, err)
	}
	appKey := strings.Repeat("b", 64)
	if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "app-wiki", Name: "cell-models"}, Data: map[string][]byte{"apiKey": []byte(appKey)}}); err != nil {
		t.Fatal(err)
	}
	render(map[string][]byte{"apiKey": []byte(providerKey), "tokensPerHour": []byte("1000")})
	var full struct {
		Config map[string]any `json:"config"`
		LLM    struct {
			Policies struct {
				APIKey struct {
					Keys []struct {
						KeyHash  string            `json:"keyHash"`
						Metadata map[string]string `json:"metadata"`
					}
				} `json:"apiKey"`
				LocalRateLimit []map[string]any `json:"localRateLimit"`
			}
		} `json:"llm"`
	}
	if err := yaml.Unmarshal(s.Data["config.yaml"], &full); err != nil {
		t.Fatal(err)
	}
	appSum := sha256.Sum256([]byte(appKey))
	ks := full.LLM.Policies.APIKey.Keys
	if len(ks) != 2 || ks[1].KeyHash != "sha256:"+hex.EncodeToString(appSum[:]) || ks[1].Metadata["account"] != "app-wiki" || ks[0].Metadata["account"] != "cell-agent" {
		t.Errorf("keys %+v", ks)
	}
	rl := full.LLM.Policies.LocalRateLimit
	if len(rl) != 1 || rl[0]["key"] != "apiKey.account" || rl[0]["maxTokens"] != float64(1000) || rl[0]["type"] != "tokens" {
		t.Errorf("rate limit %+v", rl)
	}
	if full.Config == nil {
		t.Error("no usage per account in the metrics")
	}

	// A replaced app key is taken at the next resync, the old one goes; the
	// order of the apps does not change the configuration.
	if err := c.Create(ctx, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "atlas"}, Spec: cellv1.AppSpec{Needs: cellv1.Needs{Models: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "app-atlas", Name: "cell-models"}, Data: map[string][]byte{"apiKey": []byte("atlas-key")}}); err != nil {
		t.Fatal(err)
	}
	var old corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "app-wiki", Name: "cell-models"}, &old); err != nil {
		t.Fatal(err)
	}
	newKey := strings.Repeat("c", 64)
	old.Data = map[string][]byte{"apiKey": []byte(newKey)}
	if err := c.Update(ctx, &old); err != nil {
		t.Fatal(err)
	}
	res, err = r.Reconcile(ctx, ctrl.Request{})
	if err != nil || res.RequeueAfter == 0 {
		t.Errorf("no resync: %+v %v", res, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&s), &s); err != nil {
		t.Fatal(err)
	}
	newSum := sha256.Sum256([]byte(newKey))
	conf1 := string(s.Data["config.yaml"])
	if !strings.Contains(conf1, hex.EncodeToString(newSum[:])) || strings.Contains(conf1, hex.EncodeToString(appSum[:])) {
		t.Error("the replaced key is not taken")
	}
	if strings.Index(conf1, "app-atlas") > strings.Index(conf1, "app-wiki") {
		t.Error("apps not in order")
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&s), &s); err != nil || string(s.Data["config.yaml"]) != conf1 {
		t.Errorf("configuration changed without a change: %v", err)
	}
}

// An app whose namespace has no grant for the operator yet (a held
// subdomain, say) waits; the other accounts still get their configuration.
func TestModelsAppWithoutGrant(t *testing.T) {
	ctx := context.Background()
	// The operator's real rights answer so (TestRightsStayOutOfOtherNamespaces).
	forbidden := apierrors.NewForbidden(corev1.Resource("secrets"), "cell-models", nil)
	base := fakeClient(k8s.Scheme(),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-agent"}, Data: map[string][]byte{"modelKey": []byte("agent")}},
		&cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "held"}, Spec: cellv1.AppSpec{Needs: cellv1.Needs{Models: true}}},
		&cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "wiki"}, Spec: cellv1.AppSpec{Needs: cellv1.Needs{Models: true}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "app-wiki", Name: "cell-models"}, Data: map[string][]byte{"apiKey": []byte("wiki")}})
	c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if key.Namespace == "app-held" {
			return forbidden
		}
		return c.Get(ctx, key, o, opts...)
	}})
	res, err := (&ModelsReconciler{Client: c}).Reconcile(ctx, ctrl.Request{})
	if err != nil || res.RequeueAfter != 10*time.Second {
		t.Fatalf("reconcile: %+v %v", res, err)
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: "cell-models"}, &s); err != nil || !strings.Contains(string(s.Data["config.yaml"]), "app-wiki") {
		t.Errorf("the other app is not in: %v", err)
	}
}
