package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// ModelsReconciler writes the model gateway's configuration (E-80) from the
// provider in Secret cell.ModelSettings, the agent's key in its account's
// Secret and the keys of the apps that need models (E-81), and tells what is
// set in ConfigMap cell.ModelsStatus. The
// configuration holds the provider's key, so it is a Secret too; the
// gateway reloads it when it changes.
type ModelsReconciler struct {
	client.Client
}

func (r *ModelsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	one := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: system(cell.ModelsSecret)}}
	})
	names := []string{cell.ModelSettings, cell.ModelsSecret, cell.AgentAccount}
	return ctrl.NewControllerManagedBy(mgr).
		Named("models").
		Watches(&corev1.Secret{}, one, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == cell.SystemNamespace && slices.Contains(names, o.GetName())
		}))).
		Watches(&cellv1.App{}, one, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (r *ModelsReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var agent corev1.Secret
	if err := r.Get(ctx, system(cell.AgentAccount), &agent); err != nil {
		// The operator creates it at start; the watch brings it.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var settings corev1.Secret
	if err := r.Get(ctx, system(cell.ModelSettings), &settings); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	p := cell.ReadModelProvider(settings.Data)
	view := p.View()
	if p.Check() != nil {
		view = cell.ModelsView{}
	}
	keys := []modelsKey{{account: cell.AgentAccount, key: agent.Data[cell.AgentModelKey]}}
	var apps cellv1.AppList
	if err := r.List(ctx, &apps); err != nil {
		return ctrl.Result{}, err
	}
	// Sorted, so the same keys render the same configuration. The app's own
	// reconcile makes its key; until then it gets none. Secrets in app
	// namespaces are not watched (E-70): a replaced key is picked up by the
	// next resync.
	slices.SortFunc(apps.Items, func(a, b cellv1.App) int { return strings.Compare(a.Name, b.Name) })
	var wait ctrl.Result
	for _, app := range apps.Items {
		if !app.Spec.Needs.Models || app.DeletionTimestamp != nil {
			continue
		}
		var s corev1.Secret
		err := r.Get(ctx, client.ObjectKey{Namespace: Namespace(app.Name), Name: cell.AppModelsSecret}, &s)
		// Forbidden: the app's namespace or the operator's grant in it is not
		// there yet (a held subdomain, say); the others still get in.
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			wait.RequeueAfter = 10 * time.Second
			continue
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		if wait.RequeueAfter == 0 {
			wait.RequeueAfter = time.Minute
		}
		keys = append(keys, modelsKey{account: Namespace(app.Name), key: s.Data[cell.AppModelsKey]})
	}
	conf, err := modelsConfig(p, view.Configured, keys)
	if err != nil {
		return ctrl.Result{}, err
	}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: cell.ModelsSecret}}
	err = r.Get(ctx, client.ObjectKeyFromObject(s), s)
	switch {
	case apierrors.IsNotFound(err):
		s.Type = corev1.SecretTypeOpaque
		s.Data = map[string][]byte{"config.yaml": conf}
		err = r.Create(ctx, s)
	case err == nil && string(s.Data["config.yaml"]) != string(conf):
		s.Data = map[string][]byte{"config.yaml": conf}
		err = r.Update(ctx, s)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return ctrl.Result{}, err
	}
	cm := corev1ac.ConfigMap(cell.ModelsStatus, cell.SystemNamespace).WithData(map[string]string{cell.ModelsStatusKey: string(raw), cell.ModelsVersionKey: settings.ResourceVersion})
	return wait, r.Apply(ctx, cm, client.FieldOwner(fieldOwner), client.ForceOwnership)
}

// modelsKey is a key to the gateway and the account it counts for.
type modelsKey struct {
	account string
	key     []byte
}

// modelsConfig renders the gateway's configuration: every model name goes
// to the configured model, only a known key gets through, and its tokens
// count for its account. Without a provider the gateway serves no model.
func modelsConfig(p cell.ModelProvider, configured bool, accounts []modelsKey) ([]byte, error) {
	keys := []any{}
	for _, a := range accounts {
		if len(a.key) == 0 {
			continue
		}
		sum := sha256.Sum256(a.key)
		keys = append(keys, map[string]any{"keyHash": "sha256:" + hex.EncodeToString(sum[:]), "metadata": map[string]string{"account": a.account}})
	}
	policies := map[string]any{"apiKey": map[string]any{
		// Bearer in Authorization, as the OpenAI SDKs send it and the
		// Anthropic ones with an auth token; the gateway removes it and
		// sends the provider's own key.
		"mode": "strict",
		"keys": keys,
	}}
	if p.TokensPerHour > 0 {
		// One bucket per account: a runaway agent or app stops at its own
		// limit, not at everyone's.
		policies["localRateLimit"] = []any{map[string]any{
			"type": "tokens", "key": "apiKey.account",
			"maxTokens": p.TokensPerHour, "tokensPerFill": p.TokensPerHour, "fillInterval": "1h",
		}}
	}
	models := []any{}
	if configured {
		params := map[string]any{"model": p.Model}
		if p.APIKey != "" {
			params["apiKey"] = p.APIKey
		}
		// The gateway takes baseUrl as the whole prefix: the Anthropic API's
		// is /v1, which an OpenAI-compatible address already has.
		provider := "anthropic"
		if p.BaseURL != "" {
			params["baseUrl"] = p.BaseURL + "/v1"
		}
		if p.Provider == cell.ProviderOpenAI {
			provider = "openAI"
			if p.BaseURL != "" {
				params["baseUrl"] = p.BaseURL
			}
		}
		// The defaults cache system prompt and messages where the provider
		// needs it asked (Anthropic, Bedrock); the agent sends none of it (E-93).
		models = append(models, map[string]any{"name": "*", "provider": provider, "params": params, "promptCaching": map[string]any{}})
	}
	return yaml.Marshal(map[string]any{
		// Token usage per account, read by the API (GET /v0/models/usage).
		"config": map[string]any{"metrics": map[string]any{"fields": map[string]any{"add": map[string]any{"account": "apiKey.account"}}}},
		// Agent conversations grow; the default buffer is too small for them.
		"frontendPolicies": map[string]any{"http": map[string]any{"maxBufferSize": 20 << 20}},
		"llm": map[string]any{
			"port":     3000,
			"models":   models,
			"policies": policies,
		},
	})
}
