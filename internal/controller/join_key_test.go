package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A machine that joins ends every join key made before it or in its second,
// whatever the machine says about itself; a key made after it stays, and so
// does a token that is no k3s join key (E-64).
func TestJoinKeyDeletedOnJoin(t *testing.T) {
	at := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) metav1.ObjectMeta {
		return metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(at.Add(d))}
	}
	token := func(id, group string, d time.Duration) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: stamp(d), Type: corev1.SecretTypeBootstrapToken,
			Data: map[string][]byte{"token-id": []byte(id), "token-secret": []byte(strings.Repeat("x", 16)), "auth-extra-groups": []byte(group)}}
		s.Name, s.Namespace = "bootstrap-token-"+id, "kube-system"
		return s
	}
	node := func(name string, d time.Duration) *corev1.Node {
		n := &corev1.Node{ObjectMeta: stamp(d)}
		n.Name = name
		return n
	}
	before := token("aaa111", joinGroup, -time.Minute)
	same := token("bbb222", joinGroup, 0) // the Node's own second
	after := token("ccc333", joinGroup, time.Second)
	other := token("ddd444", "system:bootstrappers:kubeadm:default-node-token", -time.Minute)
	k := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(before, same, after, other, node("first", -time.Hour), node("joined", 0)).
		WithIndex(&corev1.Secret{}, "type", func(o client.Object) []string { return []string{string(o.(*corev1.Secret).Type)} }).
		Build()
	r := &JoinKeyReconciler{Client: k, Reader: k}
	ctx := context.Background()
	left := func() map[string]bool {
		m := map[string]bool{}
		for _, s := range []*corev1.Secret{before, same, after, other} {
			err := k.Get(ctx, client.ObjectKeyFromObject(s), &corev1.Secret{})
			if err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
			m[s.Name[len("bootstrap-token-"):]] = err == nil
		}
		return m
	}
	reconcile := func(name string) {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}

	// The operator starts: the machine from before ends no newer key.
	reconcile("first")
	if m := left(); !m["aaa111"] || !m["bbb222"] || !m["ccc333"] || !m["ddd444"] {
		t.Errorf("an older machine ended keys: %v", m)
	}
	reconcile("joined")
	reconcile("joined") // twice: the keys are gone already
	if m := left(); m["aaa111"] || m["bbb222"] || !m["ccc333"] || !m["ddd444"] {
		t.Errorf("after the join: %v", m)
	}
	// A machine that is gone again: nothing to do.
	reconcile("gone")
}
