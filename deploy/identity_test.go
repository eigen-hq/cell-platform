package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/eigen-hq/cell-platform/internal/authentik"
)

// docs returns the documents of a manifest in base/ of the given kind, with
// placeholders filled in.
func docs(t *testing.T, file, kind string) []string {
	t.Helper()
	rendered := os.Expand(read(t, "base/"+file), func(string) string { return "x" })
	var out []string
	for doc := range strings.SplitSeq(rendered, "\n---\n") {
		var meta struct{ Kind string }
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if meta.Kind == kind {
			out = append(out, doc)
		}
	}
	return out
}

func platformToken(t *testing.T, where string, env []corev1.EnvVar, name string) {
	t.Helper()
	for _, e := range env {
		if e.Name != name {
			continue
		}
		if r := e.ValueFrom; r == nil || r.SecretKeyRef == nil || r.SecretKeyRef.Name != "identity-platform" || r.SecretKeyRef.Key != "token" {
			t.Errorf("%s: %s is not identity-platform/token: %+v", where, name, e)
		}
		return
	}
	t.Errorf("%s: no %s", where, name)
}

// Operator and API talk to authentik with the platform's own account, not
// the bootstrap token of akadmin (E-77); the installer creates its token, and
// authentik's worker makes the account from the blueprint with that token.
func TestPlatformUsesItsOwnAuthentikAccount(t *testing.T) {
	files, err := filepath.Glob("base/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	for _, f := range files {
		if strings.Contains(read(t, f), "AUTHENTIK_BOOTSTRAP_TOKEN") {
			t.Errorf("%s uses the bootstrap token", f)
		}
	}
	for _, f := range []string{"api.yaml", "operator.yaml"} {
		ds := docs(t, f, "Deployment")
		if len(ds) == 0 {
			t.Fatalf("%s: no Deployment", f)
		}
		for _, doc := range ds {
			var d appsv1.Deployment
			if err := yaml.UnmarshalStrict([]byte(doc), &d); err != nil {
				t.Fatal(err)
			}
			for _, c := range d.Spec.Template.Spec.Containers {
				platformToken(t, f+" "+c.Name, c.Env, "AUTHENTIK_TOKEN")
			}
		}
	}

	charts := docs(t, "identity.yaml", "HelmChart")
	if len(charts) != 1 {
		t.Fatalf("%d HelmCharts", len(charts))
	}
	var chart struct {
		Spec struct{ ValuesContent string } `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(charts[0]), &chart); err != nil {
		t.Fatal(err)
	}
	var values struct {
		Blueprints struct{ ConfigMaps []string } `json:"blueprints"`
		Worker     struct{ Env []corev1.EnvVar } `json:"worker"`
	}
	if err := yaml.Unmarshal([]byte(chart.Spec.ValuesContent), &values); err != nil {
		t.Fatal(err)
	}
	if len(values.Blueprints.ConfigMaps) != 1 || values.Blueprints.ConfigMaps[0] != "identity-blueprints" {
		t.Errorf("blueprint config maps %v", values.Blueprints.ConfigMaps)
	}
	platformToken(t, "authentik worker", values.Worker.Env, "CELL_PLATFORM_TOKEN")

	var blueprint string
	for _, doc := range docs(t, "identity.yaml", "ConfigMap") {
		var cm corev1.ConfigMap
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil {
			t.Fatal(err)
		}
		if cm.Name == "identity-blueprints" {
			blueprint = cm.Data["platform.yaml"]
		}
	}
	for _, want := range []string{
		"username: " + authentik.PlatformAccount + "\n",
		"type: service_account\n",
		"token: !Env [CELL_PLATFORM_TOKEN, null]\n",
		"key: !Context token\n",
		// Its own superuser group, declared here: no wait for another blueprint.
		"identifiers:\n      name: " + authentik.PlatformAccount + "\n    attrs:\n      is_superuser: true\n",
		"- !KeyOf platform-group\n",
	} {
		if !strings.Contains(blueprint, want) {
			t.Errorf("blueprint lacks %q:\n%s", want, blueprint)
		}
	}
	// The installer creates the token when it is missing: a fresh cell and
	// the rerun on an older one alike.
	if !strings.Contains(read(t, "bootstrap.sh"), "\nensure_secret cell-system identity-platform Opaque token=\"$(rand)\"\n") {
		t.Error("bootstrap.sh does not create Secret identity-platform")
	}
}
