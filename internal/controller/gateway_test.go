package controller

import (
	"slices"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

// The gateway offers an app's tools to the accounts that may use the
// app, and to no one else, whatever its groups are called.
func TestGatewayConfig(t *testing.T) {
	mcpApp := func(name, endpoint string, groups ...string) cellv1.App {
		a := cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: name}}
		a.Spec.MCP = &cellv1.MCP{Path: "/mcp"}
		a.Spec.Access.Groups = groups
		a.Status.MCPEndpoint = endpoint
		return a
	}
	hostile := `x" in jwt.groups || true || "y`
	leaving := mcpApp("leaving", "http://leaving.app-leaving.svc.cluster.local:80/mcp")
	leaving.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	plain := cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}
	plain.Status.MCPEndpoint = "http://plain.app-plain.svc.cluster.local:80/mcp" // left over, the manifest dropped mcp
	apps := []cellv1.App{
		mcpApp("notes", "http://notes.app-notes.svc.cluster.local:5230/mcp"),
		mcpApp("journal", "http://journal.app-journal.svc.cluster.local:5230/mcp", "team", hostile),
		mcpApp("pending", ""), // not up yet
		leaving, plain,
	}
	out, err := gatewayConfig(cell.Config{Domain: "cell.test"}, apps)
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Gateways map[string]struct{ Port int }
		Routes   []struct {
			Matches []struct {
				Path struct{ Exact string }
			}
			Backends []struct {
				MCP struct {
					StatefulMode, FailureMode, PrefixMode string
					Targets                               []struct {
						Name string
						MCP  struct{ Host string }
					}
				}
			}
			Policies struct {
				JWTAuth struct {
					Mode, Issuer string
					Audiences    []string
					JWKS         struct{ URL string }
				} `json:"jwtAuth"`
				Transformations struct {
					Request struct {
						Set    map[string]string
						Remove []string
					}
				}
				MCPAuthorization struct{ Rules []string } `json:"mcpAuthorization"`
			}
		}
	}
	if err := yaml.UnmarshalStrict([]byte(out), &conf); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(conf.Routes) != 1 || len(conf.Routes[0].Backends) != 1 || conf.Gateways["default"].Port != 3000 ||
		len(conf.Routes[0].Matches) != 1 || conf.Routes[0].Matches[0].Path.Exact != "/mcp" {
		t.Fatalf("routes: %s", out)
	}
	route := conf.Routes[0]
	backend := route.Backends[0].MCP
	var targets []string
	for _, tg := range backend.Targets {
		targets = append(targets, tg.Name+"="+tg.MCP.Host)
	}
	want := []string{"journal=http://journal.app-journal.svc.cluster.local:5230/mcp", "notes=http://notes.app-notes.svc.cluster.local:5230/mcp"}
	if len(targets) != 2 || targets[0] != want[0] || targets[1] != want[1] {
		t.Errorf("targets %v, want %v", targets, want)
	}
	if backend.StatefulMode != "stateless" || backend.FailureMode != "failOpen" || backend.PrefixMode != "always" {
		t.Errorf("backend modes: %+v", backend)
	}
	jwt := route.Policies.JWTAuth
	if jwt.Mode != "strict" || jwt.Issuer != "https://auth.cell.test/application/o/cell-mcp/" || len(jwt.Audiences) != 1 || jwt.Audiences[0] != "cell-mcp" ||
		jwt.JWKS.URL != "http://authentik-server.cell-system.svc.cluster.local/application/o/cell-mcp/jwks/" {
		t.Errorf("jwt: %+v", jwt)
	}

	// Headers of the login proxy come from the token, never the client.
	tr := route.Policies.Transformations.Request
	if len(tr.Set) != 1 || tr.Set["x-authentik-username"] != "jwt.preferred_username" ||
		!slices.Equal(tr.Remove, []string{"authorization", "x-authentik-groups", "x-authentik-email", "x-authentik-name", "x-authentik-uid"}) {
		t.Errorf("transformations: %+v", tr)
	}

	// The rules as the gateway runs them: any rule that holds allows.
	env, err := cel.NewEnv(cel.Variable("mcp", cel.DynType), cel.Variable("jwt", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	allowed := func(target string, claims map[string]any) bool {
		for _, rule := range route.Policies.MCPAuthorization.Rules {
			ast, iss := env.Compile(rule)
			if iss.Err() != nil {
				t.Fatalf("rule %q: %v", rule, iss.Err())
			}
			prg, err := env.Program(ast)
			if err != nil {
				t.Fatal(err)
			}
			v, _, err := prg.Eval(map[string]any{"mcp": map[string]any{"tool": map[string]any{"target": target}}, "jwt": claims})
			if err == nil && v == types.True {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		target string
		claims map[string]any
		want   bool
	}{
		{"notes", map[string]any{"groups": []any{}}, true},
		{"notes", map[string]any{}, true},
		{"journal", map[string]any{"groups": []any{"team"}}, true},
		{"journal", map[string]any{"groups": []any{hostile}}, true},
		{"journal", map[string]any{"groups": []any{"other"}}, false},
		{"journal", map[string]any{"groups": []any{}}, false},
		{"journal", map[string]any{}, false},
		{"pending", map[string]any{"groups": []any{"team"}}, false},
		{"plain", map[string]any{"groups": []any{"team"}}, false},
	} {
		if got := allowed(c.target, c.claims); got != c.want {
			t.Errorf("%s for %v: allowed %v, want %v", c.target, c.claims, got, c.want)
		}
	}
}
