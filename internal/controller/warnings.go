package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// WarnedAnnotation marks a task whose warning went out, with the time.
const WarnedAnnotation = "cell.internal/warned"

// warnFor is how long after an app needed attention its warning still goes
// out: an older one IT has seen in the console, or the mail server was set
// only later.
const warnFor = time.Hour

// WarningReconciler mails the cell's admins when an app needs attention
// (E-82): once per diagnose task the operator opens, through the cell's
// mail server. Without one set there is no mail; the console shows it all.
type WarningReconciler struct {
	client.Client
	// Reader reads Secrets uncached (E-70).
	Reader client.Reader
	// Recipients are the admins' mail addresses.
	Recipients func(context.Context) ([]string, error)
	// Send is cell.Mail.Send; tests replace it.
	Send func(ctx context.Context, m cell.Mail, to []string, subject, body string) error
	Now  func() time.Time
}

func (r *WarningReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// New tasks only; a restart lists every task as new, so none is missed.
	created := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).Named("warnings").
		For(&cellv1.Task{}, builder.WithPredicates(created)).Complete(r)
}

func (r *WarningReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var t cellv1.Task
	if err := r.Get(ctx, req.NamespacedName, &t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if t.Spec.Kind != "diagnose" || t.Spec.Requester.Kind != "Operator" || t.Annotations[WarnedAnnotation] != "" ||
		now.Sub(t.CreationTimestamp.Time) > warnFor {
		return ctrl.Result{}, nil
	}
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
	to, err := r.Recipients(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	warned := "nobody: no admin has a mail address"
	if len(to) > 0 {
		cfg, err := cell.LoadConfig(ctx, r.Reader)
		if err != nil {
			return ctrl.Result{}, err
		}
		cause, _, _ := strings.Cut(strings.TrimPrefix(t.Spec.Input, "Cause: "), "\n")
		subject := fmt.Sprintf("%s needs attention (%s)", t.Spec.App, cfg.Domain)
		body := fmt.Sprintf("The app %s in the cell %s needs attention.\n\nCause: %s\n\n"+
			"The ops agent is looking into it; what it proposes waits for your approval.\n\n"+
			"Open the app in the console: https://%s/apps/%s\n",
			t.Spec.App, cfg.Domain, cause, cfg.Host("console"), t.Spec.App)
		send := r.Send
		if send == nil {
			send = func(ctx context.Context, m cell.Mail, to []string, subject, body string) error {
				return m.Send(ctx, to, subject, body)
			}
		}
		// Sent before it is marked: a mark that fails sends it again, a mail
		// that fails is tried again.
		if err := send(ctx, m, to, subject, body); err != nil {
			return ctrl.Result{}, fmt.Errorf("warning for task %s: %w", t.Name, err)
		}
		warned = now.UTC().Format(time.RFC3339)
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{WarnedAnnotation: warned}}})
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.Patch(ctx, &t, client.RawPatch(types.MergePatchType, patch))
}
