package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

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

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// ModelsReconciler writes the model gateway's configuration (E-80) from the
// provider in Secret cell.ModelSettings and the agent's key in its account's
// Secret, and tells what is set in ConfigMap cell.ModelsStatus. The
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
	conf, err := modelsConfig(p, view.Configured, agent.Data[cell.AgentModelKey])
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
	return ctrl.Result{}, r.Apply(ctx, cm, client.FieldOwner(fieldOwner), client.ForceOwnership)
}

// modelsConfig renders the gateway's configuration: every model name goes
// to the configured model, and only a known key gets through. Without a
// provider the gateway serves no model.
func modelsConfig(p cell.ModelProvider, configured bool, agentKey []byte) ([]byte, error) {
	keys := []any{}
	if len(agentKey) > 0 {
		sum := sha256.Sum256(agentKey)
		keys = append(keys, map[string]any{"keyHash": "sha256:" + hex.EncodeToString(sum[:]), "metadata": map[string]string{"account": cell.AgentAccount}})
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
		models = append(models, map[string]any{"name": "*", "provider": provider, "params": params})
	}
	return yaml.Marshal(map[string]any{
		// Agent conversations grow; the default buffer is too small for them.
		"frontendPolicies": map[string]any{"http": map[string]any{"maxBufferSize": 20 << 20}},
		"llm": map[string]any{
			"port":   3000,
			"models": models,
			"policies": map[string]any{"apiKey": map[string]any{
				"mode": "strict",
				"keys": keys,
				// The Anthropic SDK sends its key there; the gateway removes it
				// and sends the provider's own.
				"location": map[string]any{"header": map[string]any{"name": "x-api-key"}},
			}},
		},
	})
}
