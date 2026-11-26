package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// MailIdentity takes the cell's mail server for invites; implemented by
// *authentik.Client.
type MailIdentity interface {
	SetMail(ctx context.Context, m cell.Mail) error
}

// MailReconciler sets the identity's mail stage from Secret
// cell.MailSettings, the one truth of the cell's mail server (E-82). A
// failed set is tried again until it holds; at start it is set once more.
type MailReconciler struct {
	client.Client
	// Reader reads Secrets uncached (E-70).
	Reader   client.Reader
	Identity MailIdentity
}

func (r *MailReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("mail").
		For(&corev1.Secret{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == cell.SystemNamespace && o.GetName() == cell.MailSettings
		}))).Complete(r)
}

func (r *MailReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var s corev1.Secret
	err := r.Reader.Get(ctx, system(cell.MailSettings), &s)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	m, err := cell.ReadMail(s.Data)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.Identity.SetMail(ctx, m)
}
