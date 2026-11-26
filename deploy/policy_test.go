package deploy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	admv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// load creates the objects of a manifest in base/ with the given names:
// the real policies and roles, not copies.
func load(t *testing.T, k client.Client, file string, names ...string) {
	t.Helper()
	raw, err := os.ReadFile("base/" + file)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	// The placeholders render.sh fills in do not matter here.
	rendered := os.Expand(string(raw), func(string) string { return "x" })
	for doc := range strings.SplitSeq(rendered, "\n---\n") {
		var meta struct {
			Kind     string
			Metadata struct{ Name string }
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatal(err)
		}
		var o client.Object
		switch meta.Kind {
		case "ValidatingAdmissionPolicy":
			o = &admv1.ValidatingAdmissionPolicy{}
		case "ValidatingAdmissionPolicyBinding":
			o = &admv1.ValidatingAdmissionPolicyBinding{}
		case "Role":
			o = &rbacv1.Role{}
		case "RoleBinding":
			o = &rbacv1.RoleBinding{}
		default:
			continue
		}
		for _, n := range names {
			if n == meta.Metadata.Name {
				if err := yaml.UnmarshalStrict([]byte(doc), o); err != nil {
					t.Fatal(err)
				}
				if err := k.Create(context.Background(), o); err != nil {
					t.Fatal(err)
				}
				found++
			}
		}
	}
	if found == 0 {
		t.Fatalf("%s: none of %v", file, names)
	}
}

// The policies on join keys (E-64): the API creates only join keys that
// expire within an hour, the operator deletes only join keys.
func TestJoinKeyPolicies(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set: run `make test`")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	admin, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	as := func(sa string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:cell-system:" + sa,
			Groups: []string{"system:serviceaccounts", "system:authenticated"}}
		k, err := client.New(c, client.Options{Scheme: scheme.Scheme})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	load(t, admin, "api.yaml", "cell-api-join-keys")
	load(t, admin, "operator.yaml", "cell-operator-join-keys")
	api, operator := as("cell-api"), as("cell-operator")
	ctx := context.Background()

	key := func(name, group, expiration string) *corev1.Secret {
		d := map[string]string{"token-id": "abc123", "token-secret": strings.Repeat("x", 16), "auth-extra-groups": group}
		if expiration != "" {
			d["expiration"] = expiration
		}
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"},
			Type: corev1.SecretTypeBootstrapToken, StringData: d}
	}
	const group = "system:bootstrappers:k3s:default-node-token"
	at := func(d time.Duration) string {
		return time.Now().Add(d).UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	create := func(s *corev1.Secret) error { return api.Create(ctx, s, client.DryRunAll) }
	// The policy takes a moment to apply.
	deadline := time.Now().Add(30 * time.Second)
	for err := create(key("bootstrap-token-abc123", group, at(2*time.Hour))); err == nil; err = create(key("bootstrap-token-abc123", group, at(2*time.Hour))) {
		if time.Now().After(deadline) {
			t.Fatal("a join key valid for two hours is accepted")
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, c := range []struct {
		name string
		key  *corev1.Secret
		deny string
	}{
		{"an hour", key("bootstrap-token-abc123", group, at(time.Hour)), ""},
		{"a minute", key("bootstrap-token-abc123", group, at(time.Minute)), ""},
		{"two hours", key("bootstrap-token-abc123", group, at(2*time.Hour)), "expires within an hour"},
		{"a year", key("bootstrap-token-abc123", group, at(365*24*time.Hour)), "expires within an hour"},
		{"no expiry", key("bootstrap-token-abc123", group, ""), "expires within an hour"},
		{"another zone", key("bootstrap-token-abc123", group, time.Now().Add(time.Hour).Format("2006-01-02T15:04:05-07:00")), "expires within an hour"},
		{"not a time", key("bootstrap-token-abc123", group, "tomorrow at noon, UTC"), "expires within an hour"},
		{"another group", key("bootstrap-token-abc123", "system:masters", at(time.Hour)), "only creates join keys"},
		{"another name", key("k3s-serving", group, at(time.Hour)), "only creates join keys"},
	} {
		err := create(c.key)
		if c.deny == "" && err != nil || c.deny != "" && (err == nil || !strings.Contains(err.Error(), "cell-api-join-keys") || !strings.Contains(err.Error(), c.deny)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	join, other := key("bootstrap-token-def456", group, at(time.Hour)), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "k3s-serving", Namespace: "kube-system"}, StringData: map[string]string{"tls.crt": "x"}}
	for _, s := range []client.Object{join, other} {
		if err := admin.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(30 * time.Second)
	for err := operator.Delete(ctx, other, client.DryRunAll); err == nil || !strings.Contains(err.Error(), "cell-operator-join-keys"); err = operator.Delete(ctx, other, client.DryRunAll) {
		if time.Now().After(deadline) {
			t.Fatalf("the operator deletes another secret in kube-system: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var keys corev1.SecretList
	if err := operator.List(ctx, &keys, client.InNamespace("kube-system"), client.MatchingFields{"type": string(corev1.SecretTypeBootstrapToken)}); err != nil || len(keys.Items) != 1 {
		t.Errorf("the operator lists the join keys: %v %d", err, len(keys.Items))
	}
	if err := operator.Delete(ctx, join); err != nil {
		t.Errorf("the operator deletes a join key: %v", err)
	}
}
