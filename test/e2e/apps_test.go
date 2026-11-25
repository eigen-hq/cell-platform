//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// referenceApp is one of the three reference apps (A-09) and how to tell
// that a browser is logged in as a given user.
type referenceApp struct {
	name    string
	timeout time.Duration
	// loginPath starts single sign-on on the app host.
	loginPath string
	// whoAmI returns the user name the app sees for the browser's session.
	whoAmI func(t *testing.T, b *http.Client, base string) string
}

var referenceApps = []referenceApp{
	{
		name: "nextcloud", timeout: 15 * time.Minute, loginPath: "/apps/user_oidc/login/1",
		whoAmI: func(t *testing.T, b *http.Client, base string) string {
			req, _ := http.NewRequest(http.MethodGet, base+"/ocs/v2.php/cloud/user?format=json", nil)
			req.Header.Set("OCS-APIRequest", "true")
			resp, err := b.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var r struct {
				OCS struct {
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				} `json:"ocs"`
			}
			_ = json.Unmarshal([]byte(readBody(t, resp)), &r)
			return r.OCS.Data.ID
		},
	},
	{
		name: "whoami", timeout: 3 * time.Minute, loginPath: "/",
		whoAmI: func(t *testing.T, b *http.Client, base string) string {
			resp, err := b.Get(base + "/")
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(readBody(t, resp), "\n") {
				if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(k, "X-Authentik-Username") {
					return strings.TrimSpace(v)
				}
			}
			return ""
		},
	},
	{
		name: "forgejo", timeout: 10 * time.Minute, loginPath: "/user/oauth2/authentik",
		whoAmI: func(t *testing.T, b *http.Client, base string) string {
			// The API wants tokens; the settings page shows the signed-in
			// user's name, or redirects to the login page.
			resp, err := b.Get(base + "/user/settings")
			if err != nil {
				t.Fatal(err)
			}
			page := readBody(t, resp)
			if resp.Request.URL.Path != "/user/settings" {
				return ""
			}
			m := regexp.MustCompile(`name="name"[^>]*value="([^"]+)"`).FindStringSubmatch(page)
			if m == nil {
				t.Logf("forgejo settings page without user name: %.300s", page)
				return ""
			}
			return m[1]
		},
	},
}

// Through the API: every reference app passes the same test.
// Installing it from the catalog makes it reachable under its subdomain
// with single sign-on, deleting its pods heals by itself, and removing it
// keeps the data so a reinstall picks it up.
func TestReferenceApps(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	const user, password = "e2e-user", "e2e-Passw0rd-not-secret"
	admin.account(t, user, password)
	for _, ra := range referenceApps {
		t.Run(ra.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			admin.installed(t, ra.name, ra.timeout)
			app := field(admin.do(t, http.MethodGet, "/v0/apps/"+ra.name, nil, http.StatusOK), "url").(string)
			base := strings.TrimSuffix(app, "/")

			b := tc.browser(t)
			landed := tc.login(t, b, base+ra.loginPath, user, password)
			page := readBody(t, landed)
			if got := ra.whoAmI(t, b, base); got != user {
				t.Fatalf("logged in as %q, want %q; login ended at %s (HTTP %d): %.300s",
					got, user, landed.Request.URL, landed.StatusCode, page)
			}
			t.Logf("%s runs at %s, %s logged in with single sign-on", ra.name, base, user)
			isolated(t, tc, ra.name)

			// A crash, played by deleting the app's pods.
			ns := "app-" + ra.name
			var pods corev1.PodList
			if err := tc.kube.List(ctx, &pods, client.InNamespace(ns)); err != nil {
				t.Fatal(err)
			}
			deleted := 0
			for _, p := range pods.Items {
				if _, isDB := p.Labels["cnpg.io/cluster"]; !isDB {
					if err := tc.kube.Delete(ctx, &p); err != nil {
						t.Fatal(err)
					}
					deleted++
				}
			}
			if deleted == 0 {
				t.Fatal("no app pod to delete")
			}
			waitFor(t, ra.timeout, func() error {
				if got := ra.whoAmI(t, b, base); got != user {
					return fmt.Errorf("after pod deletion: logged in as %q", got)
				}
				return nil
			})
			t.Logf("%s is back after its pods were deleted", ra.name)

			change := admin.propose(t, admin, http.MethodDelete, "/v0/apps/"+ra.name+"?reason=e2e", nil)
			admin.waitChange(t, change, 10*time.Minute, "Succeeded")
			admin.do(t, http.MethodGet, "/v0/apps/"+ra.name, nil, http.StatusNotFound)
			// The data stays in the cell: not visible through the API.
			var nsObj corev1.Namespace
			if err := tc.kube.Get(ctx, client.ObjectKey{Name: ns}, &nsObj); err != nil || nsObj.Labels[cellv1.LabelRetained] != "true" {
				t.Fatalf("namespace not retained: %v %v", err, nsObj.Labels)
			}
			var pvcs corev1.PersistentVolumeClaimList
			if err := tc.kube.List(ctx, &pvcs, client.InNamespace(ns)); err != nil {
				t.Fatal(err)
			}
			if wantData(loadManifest(t, ra.name)) && len(pvcs.Items) == 0 {
				t.Fatal("volumes removed with the app")
			}

			admin.installed(t, ra.name, ra.timeout)
			b = tc.browser(t)
			readBody(t, tc.login(t, b, base+ra.loginPath, user, password))
			if got := ra.whoAmI(t, b, base); got != user {
				t.Fatalf("after reinstall: logged in as %q", got)
			}
			t.Logf("%s removed (data kept in the cell) and reinstalled, login works again", ra.name)
		})
	}
}

// isolated checks that only the platform reaches the app: a pod outside it
// (here in default) cannot call the app's service past the ingress and its
// login, a pod in the app's own namespace can, and the namespace refuses
// privileged pods.
func isolated(t *testing.T, tc *testCell, app string) {
	t.Helper()
	ns := "app-" + app
	var ings networkingv1.IngressList
	if err := tc.kube.List(context.Background(), &ings, client.InNamespace(ns), client.MatchingLabels{cellv1.LabelApp: app}); err != nil {
		t.Fatal(err)
	}
	var target string
	for _, ing := range ings.Items {
		for _, rule := range ing.Spec.Rules {
			for _, p := range rule.HTTP.Paths {
				if b := p.Backend.Service; b != nil && p.Path == "/" {
					target = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/", b.Name, ns, b.Port.Number)
				}
			}
		}
	}
	if target == "" {
		t.Fatalf("no ingress backend for %s", app)
	}
	probe := func(from string) string {
		t.Helper()
		// Only a refused or silent connection means the network kept the
		// call out; an HTTP answer, even an error, means it got through.
		out, err := exec.Command(filepath.Join(repoRoot(), "bin", "kubectl"), "-n", from, "run", "probe-"+app, "--rm", "-i", "--quiet",
			"--restart=Never", "--pod-running-timeout=2m", "--image=busybox:1.37", "--",
			"sh", "-c", "wget -q -T 5 -O /dev/null "+target+" 2>&1 && echo PROBE:answered; echo PROBE:done").CombinedOutput()
		text := string(out)
		switch {
		case err != nil || !strings.Contains(text, "PROBE:done"):
			t.Fatalf("probe from %s did not run: %v %s", from, err, text)
		case strings.Contains(text, "PROBE:answered") || strings.Contains(text, "server returned error"):
			return "reached"
		case strings.Contains(text, "Connection refused") || strings.Contains(text, "timed out"):
			return "blocked"
		}
		t.Fatalf("probe from %s: unclear answer %s", from, text)
		return ""
	}
	if got := probe(ns); got != "reached" {
		t.Errorf("%s from its own namespace: %s", target, got)
	}
	if got := probe("default"); got != "blocked" {
		t.Errorf("%s from outside the platform: %s", target, got)
	}
	// A chart's deployer is admin of its namespace only; a release it writes
	// without naming an account runs as that deployer too.
	if strings.Contains(kubectl(t, "-n", ns, "get", "serviceaccounts", "-o", "name"), "helm-deployer") {
		as := "--as=system:serviceaccount:" + ns + ":helm-deployer"
		for _, q := range [][]string{{"create", "clusterrolebindings"}, {"get", "secrets", "-n", "cell-system"}, {"create", "pods", "-n", "default"}} {
			out, _ := exec.Command(filepath.Join(repoRoot(), "bin", "kubectl"), append([]string{"auth", "can-i", as}, q...)...).Output()
			if strings.TrimSpace(string(out)) != "no" {
				t.Errorf("helm-deployer of %s can %v: %s", app, q, out)
			}
		}
		if args := kubectl(t, "-n", "flux-system", "get", "deploy", "helm-controller", "-o", "jsonpath={.spec.template.spec.containers[0].args}"); !strings.Contains(args, "--default-service-account=helm-deployer") || !strings.Contains(args, "--no-cross-namespace-refs=true") {
			t.Errorf("helm-controller args %s", args)
		}
	}
	out, err := exec.Command(filepath.Join(repoRoot(), "bin", "kubectl"), "-n", ns, "run", "privileged", "--image=busybox:1.37",
		"--privileged", "--restart=Never", "--dry-run=server").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "PodSecurity") {
		t.Errorf("privileged pod in %s: %v %s", ns, err, out)
	}
}

func wantData(app *cellv1.App) bool {
	return app.Spec.Needs.Database != nil || len(app.Spec.Needs.Volumes) > 0 || app.Spec.Source.Helm != nil
}

func loadManifest(t *testing.T, name string) *cellv1.App {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(), "catalog", name, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var app cellv1.App
	if err := yaml.UnmarshalStrict(data, &app); err != nil {
		t.Fatal(err)
	}
	return &app
}

func waitFor(t *testing.T, timeout time.Duration, f func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := f()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
	}
}
