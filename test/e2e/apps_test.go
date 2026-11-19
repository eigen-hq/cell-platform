//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
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

// M1: every reference app passes the same test. Installing it from its
// manifest makes it reachable under its subdomain with single sign-on,
// deleting its pods heals by itself, and removing it keeps the data so a
// reinstall picks it up.
func TestReferenceApps(t *testing.T) {
	tc := newTestCell(t)
	const user, password = "e2e-user", "e2e-Passw0rd-not-secret"
	tc.user(t, user, password)
	for _, ra := range referenceApps {
		t.Run(ra.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			app := loadManifest(t, ra.name)
			install(t, tc, app, ra.timeout)
			base := "https://" + tc.cfg.Host(app.Spec.Access.Subdomain)

			b := tc.browser(t)
			landed := tc.login(t, b, base+ra.loginPath, user, password)
			page := readBody(t, landed)
			if got := ra.whoAmI(t, b, base); got != user {
				t.Fatalf("logged in as %q, want %q; login ended at %s (HTTP %d): %.300s",
					got, user, landed.Request.URL, landed.StatusCode, page)
			}

			ns := "app-" + app.Name
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

			if err := tc.kube.Delete(ctx, app); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 3*time.Minute, func() error {
				err := tc.kube.Get(ctx, client.ObjectKey{Name: app.Name}, &cellv1.App{})
				if apierrors.IsNotFound(err) {
					return nil
				}
				return fmt.Errorf("app still exists: %v", err)
			})
			var nsObj corev1.Namespace
			if err := tc.kube.Get(ctx, client.ObjectKey{Name: ns}, &nsObj); err != nil || nsObj.Labels[cellv1.LabelRetained] != "true" {
				t.Fatalf("namespace not retained: %v %v", err, nsObj.Labels)
			}
			var pvcs corev1.PersistentVolumeClaimList
			if err := tc.kube.List(ctx, &pvcs, client.InNamespace(ns)); err != nil {
				t.Fatal(err)
			}
			if wantData(app) && len(pvcs.Items) == 0 {
				t.Fatal("volumes removed with the app")
			}

			install(t, tc, loadManifest(t, ra.name), ra.timeout)
			b = tc.browser(t)
			readBody(t, tc.login(t, b, base+ra.loginPath, user, password))
			if got := ra.whoAmI(t, b, base); got != user {
				t.Fatalf("after reinstall: logged in as %q", got)
			}
		})
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

// install creates the app (or leaves an existing one) and waits until Ready.
func install(t *testing.T, tc *testCell, app *cellv1.App, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	if err := tc.kube.Create(ctx, app); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	waitFor(t, timeout, func() error {
		var a cellv1.App
		if err := tc.kube.Get(ctx, client.ObjectKey{Name: app.Name}, &a); err != nil {
			return err
		}
		// Healthy for its current spec; NeedsAttention after an earlier
		// failed change still counts as running.
		c := apimeta.FindStatusCondition(a.Status.Conditions, "Ready")
		if c == nil || c.Status != metav1.ConditionTrue || c.ObservedGeneration != a.Generation {
			return fmt.Errorf("%s is %s: %+v", app.Name, a.Status.Phase, a.Status.Conditions)
		}
		return nil
	})
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
