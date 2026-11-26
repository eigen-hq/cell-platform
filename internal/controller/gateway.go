package controller

import (
	"context"
	"slices"
	"strconv"
	"strings"

	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

const (
	// GatewayConfig is the ConfigMap (in the system namespace) the MCP
	// gateway reads its configuration from.
	GatewayConfig = "cell-mcp"
	// GatewayClient is the identity's client the gateway's tokens are for.
	GatewayClient = "cell-mcp"
	// GatewayTokenValidity is how long a token for the gateway lasts. The
	// gateway checks tokens offline (signature and expiry, no revocation),
	// so this bounds how long a stolen token or a removed access still
	// works (E-74).
	GatewayTokenValidity = "minutes=5"
)

// GatewayReconciler writes the configuration of the MCP gateway
// (agentgateway): one target per app with an MCP endpoint, open to the
// accounts that may use the app. The gateway reloads the file when it
// changes.
type GatewayReconciler struct {
	client.Client
}

func (r *GatewayReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	cfg, err := cell.LoadConfig(ctx, r)
	if err != nil {
		return ctrl.Result{}, err
	}
	var apps cellv1.AppList
	if err := r.List(ctx, &apps); err != nil {
		return ctrl.Result{}, err
	}
	conf, err := gatewayConfig(cfg, apps.Items)
	if err != nil {
		return ctrl.Result{}, err
	}
	cm := corev1ac.ConfigMap(GatewayConfig, cell.SystemNamespace).WithData(map[string]string{"config.yaml": conf})
	return ctrl.Result{}, r.Apply(ctx, cm, client.FieldOwner(fieldOwner), client.ForceOwnership)
}

func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	one := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: GatewayConfig}}}
	})
	// Once at start: a cell without apps still needs a configuration.
	start := make(chan event.GenericEvent, 1)
	start <- event.GenericEvent{Object: &cellv1.App{}}
	return ctrl.NewControllerManagedBy(mgr).
		Named("gateway").
		Watches(&cellv1.App{}, one).
		WatchesRawSource(crsource.Channel(start, one)).
		Complete(r)
}

// gatewayConfig renders the gateway's configuration. Tokens must come from
// the identity for the gateway's client; each target is open to the app's
// access groups (to everyone if it has none), the rule the app's login
// follows. Tool names start with the app's name.
func gatewayConfig(cfg cell.Config, apps []cellv1.App) (string, error) {
	apps = slices.Clone(apps)
	slices.SortFunc(apps, func(a, b cellv1.App) int { return strings.Compare(a.Name, b.Name) })
	targets := []any{}
	rules := []string{}
	for _, app := range apps {
		if app.Spec.MCP == nil || app.Status.MCPEndpoint == "" || !app.DeletionTimestamp.IsZero() {
			continue
		}
		targets = append(targets, map[string]any{"name": app.Name, "mcp": map[string]any{"host": app.Status.MCPEndpoint}})
		rule := "mcp.tool.target == " + strconv.Quote(app.Name)
		if groups := app.Spec.Access.Groups; len(groups) > 0 {
			quoted := make([]string, len(groups))
			for i, g := range groups {
				quoted[i] = strconv.Quote(g)
			}
			rule += " && [" + strings.Join(quoted, ", ") + "].exists(g, g in jwt.groups)"
		}
		rules = append(rules, rule)
	}
	// The gateway passes the client's headers on to the app. Apps behind
	// the login proxy may trust its headers, so the gateway sets them from
	// the token: the username, the rest removed. The token itself stays at
	// the gateway (it checks it before any transformation).
	remove := []string{"authorization"}
	for _, h := range proxyHeaders[1:] {
		remove = append(remove, strings.ToLower(h))
	}
	identity := "/application/o/" + GatewayClient + "/"
	conf := map[string]any{
		"gateways": map[string]any{"default": map[string]any{"port": 3000}},
		"routes": []any{map[string]any{
			"matches": []any{map[string]any{"path": map[string]any{"exact": "/mcp"}}},
			"backends": []any{map[string]any{"mcp": map[string]any{
				// Each request stands alone, so a gateway restart loses nothing;
				// an app that is down drops out instead of failing the rest.
				"statefulMode": "stateless",
				"failureMode":  "failOpen",
				// Tool names keep their app prefix however many apps there are.
				"prefixMode": "always",
				"targets":    targets,
			}}},
			"policies": map[string]any{
				"jwtAuth": map[string]any{
					"mode":      "strict",
					"issuer":    "https://" + cfg.Host("auth") + identity,
					"audiences": []string{GatewayClient},
					"jwks":      map[string]any{"url": "http://" + authentikSvc + "." + cell.SystemNamespace + ".svc.cluster.local" + identity + "jwks/"},
				},
				"transformations": map[string]any{"request": map[string]any{
					"set":    map[string]string{strings.ToLower(proxyHeaders[0]): "jwt.preferred_username"},
					"remove": remove,
				}},
				"mcpAuthorization": map[string]any{"rules": rules},
			},
		}},
	}
	out, err := yaml.Marshal(conf)
	return string(out), err
}
