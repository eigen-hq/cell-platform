//go:build e2e

// End-to-end tests against the local cell (make cell-up).
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
)

func repoRoot() string {
	_, file, _, _ := goruntime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func kube(t *testing.T) client.Client {
	t.Helper()
	ctrllog.SetLogger(logr.Discard())
	cfg, err := ctrl.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := cellv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = repoRoot()
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// Every base service is healthy and the login page answers.
func TestCellStatus(t *testing.T) {
	t.Log(run(t, "go", "run", "./cmd/cellctl", "status"))
}

// Running the bootstrap a second time changes nothing.
func TestBootstrapIdempotent(t *testing.T) {
	c := kube(t)
	before := desiredState(t, c)
	run(t, "deploy/local-up.sh")
	after := desiredState(t, c)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed: %s -> %s", k, v, after[k])
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("%s created by second run", k)
		}
	}
}

// desiredState maps every object the bootstrap manages to its generation
// (spec changes) or, for objects without one, its resourceVersion.
// Objects owned by controllers are skipped: they are not written by us.
func desiredState(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	kinds := []struct {
		gvk schema.GroupVersionKind
		ns  string
	}{
		{schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, ""},
		{schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}, ""},
		{schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "ClusterIssuer"}, ""},
		{schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}, ""},
		{schema.GroupVersionKind{Group: "helm.cattle.io", Version: "v1", Kind: "HelmChart"}, ""},
		{schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}, ""},
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, ""},
		{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "cell-system"},
		{schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, "cell-system"},
	}
	state := map[string]string{}
	for _, k := range kinds {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(k.gvk.GroupVersion().WithKind(k.gvk.Kind + "List"))
		if err := c.List(context.Background(), list, client.InNamespace(k.ns)); err != nil {
			t.Fatalf("list %s: %v", k.gvk.Kind, err)
		}
		for _, o := range list.Items {
			if len(o.GetOwnerReferences()) > 0 {
				continue
			}
			v := o.GetResourceVersion()
			if g := o.GetGeneration(); g > 0 {
				v = fmt.Sprintf("gen %d", g)
			}
			state[fmt.Sprintf("%s %s/%s", k.gvk.Kind, o.GetNamespace(), o.GetName())] = v
		}
	}
	return state
}
