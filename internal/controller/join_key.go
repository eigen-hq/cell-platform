package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// joinGroup is the group k3s gives machines that join with a token.
const joinGroup = "system:bootstrappers:k3s:default-node-token"

// JoinKeyReconciler deletes the join keys once a machine has joined: every
// k3s join token made before the machine's Node or in its second (E-64).
// The Node is the first sign of a join that nothing on the machine can
// leave out and that comes after the machine's last request with the key;
// k3s writes the node password earlier, but the machine still asks for
// certificates with the key after that.
type JoinKeyReconciler struct {
	client.Client
	// Reader lists the keys without a cache of the secrets in kube-system.
	Reader client.Reader
}

func (r *JoinKeyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var keys corev1.SecretList
	err := r.Reader.List(ctx, &keys, client.InNamespace(metav1.NamespaceSystem),
		client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("type", string(corev1.SecretTypeBootstrapToken))})
	if err != nil {
		return ctrl.Result{}, err
	}
	for i := range keys.Items {
		key := &keys.Items[i]
		// The times have whole seconds: a key from the Node's own second may
		// be the one it joined with, so it ends too.
		if string(key.Data["auth-extra-groups"]) != joinGroup || node.CreationTimestamp.Before(&key.CreationTimestamp) {
			continue
		}
		uid := key.UID
		err := r.Delete(ctx, key, client.Preconditions{UID: &uid})
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			continue // gone, or a new key under the same name
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		ctrl.LoggerFrom(ctx).Info("deleted a join key after a machine joined", "key", key.Name)
	}
	return ctrl.Result{}, nil
}

func (r *JoinKeyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// A new Node, and every Node once when the operator starts (the key
	// cut-off is the Node's creation time, so that is harmless).
	created := predicate.Funcs{
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("joinkey").
		For(&corev1.Node{}, builder.WithPredicates(created)).
		Complete(r)
}
