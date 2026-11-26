package controller

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// The suite runs the operator under the rights deploy/base gives it (E-70);
// these tests pin what those rights leave out.

const (
	operatorAccount = "system:serviceaccount:cell-system:cell-operator"
	apiAccount      = "system:serviceaccount:cell-system:cell-api"
)

var (
	restCfg *rest.Config
	kubectl func(args ...string) error
	// The manifests the installer applies the rights from.
	rightsFiles = []string{"../../deploy/base/operator.yaml", "../../deploy/base/api.yaml"}
)

var kindLine = regexp.MustCompile(`(?m)^kind: (\S+)$`)

var accountGroups = []string{"system:serviceaccounts", "system:serviceaccounts:cell-system", "system:authenticated"}

// as is the API server's config for a ServiceAccount of cell-system.
func as(user string) *rest.Config {
	c := rest.CopyConfig(restCfg)
	c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: accountGroups}
	return c
}

func setUpRights(env *envtest.Environment) error {
	admin, err := env.AddUser(envtest.User{Name: "rights-admin", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		return err
	}
	kc, err := admin.Kubectl()
	if err != nil {
		return err
	}
	kubectl = func(args ...string) error {
		_, stderr, err := kc.Run(args...)
		if err != nil {
			msg, _ := io.ReadAll(stderr)
			return fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil
	}
	for _, ns := range []string{"cell-system", "cert-manager"} {
		if err := k8s.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			return err
		}
	}
	return applyRights(rightsFiles...)
}

// applyRights applies the RBAC and admission policies of the manifests with
// kubectl's client-side apply, as the installer does (deploy/lib.sh).
func applyRights(files ...string) error {
	kinds := map[string]bool{"ServiceAccount": true, "ClusterRole": true, "ClusterRoleBinding": true, "Role": true,
		"RoleBinding": true, "ValidatingAdmissionPolicy": true, "ValidatingAdmissionPolicyBinding": true}
	var docs []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, d := range strings.Split(string(raw), "\n---\n") {
			// The other documents hold the installer's placeholders and are
			// no YAML until it renders them.
			if m := kindLine.FindStringSubmatch(d); m != nil && kinds[m[1]] {
				docs = append(docs, d)
			}
		}
	}
	tmp, err := os.CreateTemp("", "rights-*.yaml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(strings.Join(docs, "\n---\n")); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return kubectl("apply", "-f", tmp.Name())
}

func allowed(ctx context.Context, user string, a authorizationv1.ResourceAttributes) (bool, error) {
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: user, Groups: accountGroups, ResourceAttributes: &a,
	}}
	if err := k8s.Create(ctx, sar); err != nil {
		return false, err
	}
	return sar.Status.Allowed, nil
}

// denied lists what an account may not do in the cell.
func denied(ctx context.Context, user string, attrs []authorizationv1.ResourceAttributes) error {
	for _, a := range attrs {
		ok, err := allowed(ctx, user, a)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("%s may %s %s/%s %s in %q", user, a.Verb, a.Resource, a.Subresource, a.Name, a.Namespace)
		}
	}
	return nil
}

var (
	operatorDenied = []authorizationv1.ResourceAttributes{
		{Namespace: "kube-system", Verb: "get", Resource: "secrets"},
		{Namespace: "default", Verb: "create", Resource: "configmaps"},
		{Namespace: "default", Verb: "create", Resource: "deployments", Group: "apps"},
		{Namespace: "default", Verb: "create", Resource: "jobs", Group: "batch"},
		{Namespace: "default", Verb: "delete", Resource: "rolebindings", Group: "rbac.authorization.k8s.io"},
		{Namespace: "kube-system", Verb: "list", Resource: "pods"},
		{Namespace: "default", Verb: "create", Resource: "events"},
		{Namespace: "cell-system", Verb: "update", Resource: "secrets", Name: "identity"},
		{Namespace: "cert-manager", Verb: "get", Resource: "secrets", Name: "letsencrypt"},
		{Verb: "bind", Resource: "clusterroles", Group: "rbac.authorization.k8s.io", Name: "cluster-admin"},
	}
	apiDenied = []authorizationv1.ResourceAttributes{
		{Namespace: "kube-system", Verb: "list", Resource: "pods"},
		{Namespace: "kube-system", Verb: "get", Resource: "pods", Subresource: "log"},
		{Namespace: "default", Verb: "list", Resource: "events"},
		{Namespace: "cell-system", Verb: "get", Resource: "secrets"},
	}
)

func TestRightsStayOutOfOtherNamespaces(t *testing.T) {
	ctx := context.Background()
	eventually(t, func() error { return denied(ctx, operatorAccount, operatorDenied) })
	eventually(t, func() error { return denied(ctx, apiAccount, apiDenied) })
}

// The live cells of v0.2.4 and v0.2.5 hold the old cluster-wide rights; an
// installer rerun has to take them back, not add to them.
func TestInstallerRerunTakesRightsBack(t *testing.T) {
	ctx := context.Background()
	if err := applyRights("testdata/rbac-v0.2.5.yaml"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := applyRights(rightsFiles...); err != nil {
			t.Error(err)
		}
	})
	eventually(t, func() error {
		if err := denied(ctx, operatorAccount, operatorDenied[:1]); err == nil {
			return fmt.Errorf("the old rights did not apply")
		}
		return nil
	})
	if err := applyRights(rightsFiles...); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error { return denied(ctx, operatorAccount, operatorDenied) })
	eventually(t, func() error { return denied(ctx, apiAccount, apiDenied) })
}

// The operator writes namespaces and RoleBindings across the cluster, but the
// policy cell-operator-scope keeps both in app-* and to its three grants.
func TestOperatorScopePolicy(t *testing.T) {
	ctx := context.Background()
	op, err := client.New(as(operatorAccount), client.Options{Scheme: k8s.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	baseline := map[string]string{"pod-security.kubernetes.io/enforce": "baseline"}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-scope", Labels: baseline}}
	if err := k8s.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	binding := func(ns, role, account, accountNS string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: account, Namespace: accountNS}},
		}
	}
	for name, o := range map[string]client.Object{
		"namespace outside app-*":         &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "scope-other", Labels: baseline}},
		"namespace without baseline":      &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-scope-privileged"}},
		"admin for the operator":          binding("app-scope", "admin", "cell-operator", "cell-system"),
		"admin for another namespace":     binding("app-scope", "admin", "helm-deployer", "kube-system"),
		"helm-deployer outside app-*":     binding("kube-system", "admin", "helm-deployer", "kube-system"),
		"operator's grant to the API":     binding("app-scope", "cell-operator-app", "cell-api", "cell-system"),
		"API's grant outside app-*":       binding("cell-system", "cell-api-app", "cell-api", "cell-system"),
		"operator's grant in kube-system": binding("kube-system", "cell-operator-app", "cell-operator", "cell-system"),
	} {
		eventually(t, func() error {
			err := op.Create(ctx, o, client.DryRunAll)
			if err == nil || !strings.Contains(err.Error(), "cell-operator-scope") {
				return fmt.Errorf("%s: want the policy to deny it, got %v", name, err)
			}
			return nil
		})
	}
	// A namespace of a cell's own services keeps its labels.
	kube := &corev1.Namespace{}
	if err := op.Get(ctx, client.ObjectKey{Name: "kube-system"}, kube); err != nil {
		t.Fatal(err)
	}
	before := kube.DeepCopy()
	kube.Labels = baseline
	if err := op.Patch(ctx, kube, client.MergeFrom(before), client.DryRunAll); err == nil || !strings.Contains(err.Error(), "cell-operator-scope") {
		t.Fatalf("relabel kube-system: want the policy to deny it, got %v", err)
	}

	// The grants themselves pass, and the API reads the app's pods through
	// its own, nothing beyond.
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "scope"}}
	for _, g := range grants(app) {
		if err := op.Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	api, err := client.New(as(apiAccount), client.Options{Scheme: k8s.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error { return api.List(ctx, &corev1.PodList{}, client.InNamespace("app-scope")) })
	if err := api.List(ctx, &corev1.PodList{}, client.InNamespace("kube-system")); !apierrors.IsForbidden(err) {
		t.Fatalf("API lists pods in kube-system: want forbidden, got %v", err)
	}
	if err := api.List(ctx, &corev1.SecretList{}, client.InNamespace("app-scope")); !apierrors.IsForbidden(err) {
		t.Fatalf("API lists secrets of an app: want forbidden, got %v", err)
	}
}
