package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
)

// changeFinalizer keeps a running change until it ended.
var changeFinalizer = cellv1.GroupVersion.Group + "/change"

// ChangeReconciler executes ChangeRequests, one per app at a time: wait for
// approval, back up, apply the patch, wait for the health check and roll
// back to the backup (spec and data) if it does not pass in time.
type ChangeReconciler struct {
	client.Client
	Now func() time.Time
}

func (r *ChangeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// A change waits for its backup, its app and its restore.
	byChange := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		name := o.GetLabels()[labelChange]
		if rs, ok := o.(*cellv1.Restore); ok {
			name = rs.Spec.Change
		}
		if name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: name}}}
	})
	return ctrl.NewControllerManagedBy(mgr).For(&cellv1.ChangeRequest{}).
		Watches(&cellv1.Backup{}, byChange).
		Watches(&cellv1.Restore{}, byChange).
		Watches(&cellv1.App{}, handler.EnqueueRequestsFromMapFunc(r.changesOf)).
		Complete(r)
}

// changesOf maps an app to its running changes.
func (r *ChangeReconciler) changesOf(ctx context.Context, o client.Object) []reconcile.Request {
	var changes cellv1.ChangeRequestList
	if err := r.List(ctx, &changes); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, cr := range changes.Items {
		if cr.Spec.App == o.GetName() && !finished(cr.Status.Phase) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKey{Name: cr.Name}})
		}
	}
	return reqs
}

func (r *ChangeReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ChangeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cr cellv1.ChangeRequest
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	holder := "ChangeRequest/" + cr.Name
	// A started change (it holds the app) is not given up halfway: deleting
	// it waits until it ended. Waiting or ended changes go at once.
	if !cr.DeletionTimestamp.IsZero() && cr.Status.Phase != cellv1.RunRunning {
		if err := releaseApp(ctx, r, cr.Spec.App, holder); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&cr, changeFinalizer) {
			return ctrl.Result{}, r.Update(ctx, &cr)
		}
		return ctrl.Result{}, nil
	}
	if cr.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(&cr, changeFinalizer) {
		return ctrl.Result{Requeue: true}, r.Update(ctx, &cr)
	}
	if finished(cr.Status.Phase) {
		return ctrl.Result{}, releaseApp(ctx, r, cr.Spec.App, holder)
	}
	set := func(phase cellv1.RunPhase, msg string, after time.Duration) (ctrl.Result, error) {
		if cr.Status.Phase == phase && cr.Status.Message == msg {
			return ctrl.Result{RequeueAfter: after}, nil
		}
		cr.Status.Phase, cr.Status.Message = phase, msg
		return ctrl.Result{RequeueAfter: after}, r.Status().Update(ctx, &cr)
	}
	end := func(phase cellv1.RunPhase, msg string) (ctrl.Result, error) {
		// The change's backup runs under the change's hold on the app: it is
		// cancelled, its job stopped, before the change lets go of the app.
		if running, err := r.cancelBackup(ctx, &cr); err != nil || running {
			if err != nil {
				return ctrl.Result{}, err
			}
			return set(cr.Status.Phase, "stopping backup "+cr.Status.Backup+" before ending: "+msg, 2*time.Second)
		}
		cr.Status.Phase, cr.Status.Message, cr.Status.FinishedAt = phase, msg, ptr(metav1.NewTime(r.now()))
		if err := r.Status().Update(ctx, &cr); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, releaseApp(ctx, r, cr.Spec.App, holder)
	}
	wait := 5 * time.Second

	var app cellv1.App
	if err := r.Get(ctx, client.ObjectKey{Name: cr.Spec.App}, &app); err != nil {
		if apierrors.IsNotFound(err) {
			return end(cellv1.RunFailed, "app "+cr.Spec.App+" does not exist")
		}
		return ctrl.Result{}, err
	}

	removing := !app.DeletionTimestamp.IsZero()
	if cr.Status.Phase != cellv1.RunRunning {
		switch {
		case removing:
			return end(cellv1.RunFailed, "app "+app.Name+" is being removed")
		case cr.Spec.Approval.Decision == "Rejected":
			return end(cellv1.RunRejected, "rejected by "+cr.Spec.Approval.By)
		case cr.Spec.Approval.Required && cr.Spec.Approval.Decision != "Approved":
			return set(cellv1.RunWaitingForApproval, "waiting for approval", 0)
		}
		ok, current, err := acquireApp(ctx, r, &app, holder)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ok {
			return set(cellv1.RunPending, "waiting for "+current, wait)
		}
		cr.Status.StartedAt, cr.Status.AppUID = ptr(metav1.NewTime(r.now())), string(app.UID)
		return set(cellv1.RunRunning, "backing up", time.Second)
	}
	if cr.Status.AppUID != "" && cr.Status.AppUID != string(app.UID) {
		return end(cellv1.RunFailed, "app "+app.Name+" was removed and reinstalled while the change ran")
	}
	// Removing the app ends the change; only a rollback that is replacing
	// data runs to its end first.
	if removing && cr.Status.Restore == "" {
		return end(cellv1.RunFailed, "app "+app.Name+" was removed while the change ran")
	}

	// 1. Rollback point.
	if cr.Status.Backup == "" {
		cr.Status.Backup = "none"
		if hasData(&app) {
			cr.Status.Backup = cr.Name + "-pre"
		}
		return ctrl.Result{RequeueAfter: time.Second}, r.Status().Update(ctx, &cr)
	}
	if cr.Status.PreviousSpec == nil && cr.Status.Backup != "none" {
		// Bound to this change's UID: a backup left by an earlier change of
		// the same name is replaced, never taken as this change's.
		b := &cellv1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: cr.Status.Backup, Labels: runLabels(map[string]string{cellv1.LabelApp: app.Name}, labelChange, cr.Name)},
			Spec:       cellv1.BackupSpec{App: app.Name, Reason: "pre-change"},
		}
		if ok, err := ensureChild(ctx, r, b, string(cr.UID)); err != nil || !ok {
			return ctrl.Result{RequeueAfter: wait}, err
		}
		switch b.Status.Phase {
		case cellv1.RunCompleted:
		case cellv1.RunFailed:
			return end(cellv1.RunFailed, "backup before the change failed, nothing changed: "+b.Status.Message)
		default:
			return set(cellv1.RunRunning, "backing up", wait)
		}
	}

	// 2. The change itself. The original spec is recorded before the app
	// is touched, so a restart in between resumes instead of losing it.
	if cr.Status.PreviousSpec == nil {
		if cr.Spec.Patch == nil {
			return end(cellv1.RunFailed, "no patch")
		}
		prev, err := json.Marshal(app.Spec)
		if err != nil {
			return ctrl.Result{}, err
		}
		cr.Status.PreviousSpec, cr.Status.BaseGeneration = &apiextensionsv1.JSON{Raw: prev}, app.Generation
		cr.Status.Message = "applying the change"
		return ctrl.Result{RequeueAfter: time.Second}, r.Status().Update(ctx, &cr)
	}
	if cr.Status.AppliedGeneration == 0 {
		next, err := jsonpatch.MergePatch(cr.Status.PreviousSpec.Raw, cr.Spec.Patch.Raw)
		if err != nil {
			return end(cellv1.RunFailed, "patch: "+err.Error())
		}
		var spec cellv1.AppSpec
		if err := json.Unmarshal(next, &spec); err != nil {
			return end(cellv1.RunFailed, "patched spec: "+err.Error())
		}
		cur, err := json.Marshal(app.Spec)
		if err != nil {
			return ctrl.Result{}, err
		}
		if want, _ := json.Marshal(spec); !sameJSON(cur, want) {
			app.Spec = spec
			if err := r.Update(ctx, &app); err != nil {
				if apierrors.IsInvalid(err) {
					return end(cellv1.RunFailed, "patched spec rejected: "+err.Error())
				}
				return ctrl.Result{}, err
			}
		}
		if app.Generation == cr.Status.BaseGeneration {
			return end(cellv1.RunSucceeded, "nothing to change")
		}
		cr.Status.AppliedGeneration, cr.Status.AppliedAt = app.Generation, ptr(metav1.NewTime(r.now()))
		cr.Status.Message = "waiting for the health check"
		return ctrl.Result{RequeueAfter: wait}, r.Status().Update(ctx, &cr)
	}

	// 3. Health check, or rollback once the app had its time.
	if cr.Status.Restore == "" {
		if appReadyAt(&app, cr.Status.AppliedGeneration) {
			return end(cellv1.RunSucceeded, "app healthy with the change")
		}
		timeout := time.Duration(app.Spec.Health.TimeoutSeconds) * time.Second
		if timeout == 0 {
			timeout = 300 * time.Second
		}
		if r.now().Sub(cr.Status.AppliedAt.Time) < timeout {
			return set(cellv1.RunRunning, "waiting for the health check", wait)
		}
		reason := "no condition"
		if c := apimeta.FindStatusCondition(app.Status.Conditions, condReady); c != nil {
			reason = c.Message
		}
		// Record the decision first: a restart from here on resumes the
		// rollback instead of mistaking the reverted app for success.
		cr.Status.Restore = "none"
		if cr.Status.Backup != "none" {
			cr.Status.Restore = cr.Name + "-rollback"
		}
		cr.Status.Message = fmt.Sprintf("unhealthy after %s (%s), rolling back", timeout, reason)
		return ctrl.Result{RequeueAfter: time.Second}, r.Status().Update(ctx, &cr)
	}

	// 4. Rollback: previous spec, then the data of the backup.
	var prev cellv1.AppSpec
	if err := json.Unmarshal(cr.Status.PreviousSpec.Raw, &prev); err != nil {
		return ctrl.Result{}, err
	}
	if cur, err := json.Marshal(app.Spec); err != nil {
		return ctrl.Result{}, err
	} else if !sameJSON(cur, cr.Status.PreviousSpec.Raw) {
		app.Spec = prev
		if err := r.Update(ctx, &app); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if cr.Status.Restore != "none" {
		rs := &cellv1.Restore{
			ObjectMeta: metav1.ObjectMeta{Name: cr.Status.Restore, Labels: runLabels(map[string]string{cellv1.LabelApp: app.Name}, labelChange, cr.Name)},
			Spec:       cellv1.RestoreSpec{App: app.Name, Backup: cr.Status.Backup, Change: cr.Name},
		}
		if ok, err := ensureChild(ctx, r, rs, string(cr.UID)); err != nil || !ok {
			return ctrl.Result{RequeueAfter: wait}, err
		}
		switch rs.Status.Phase {
		case cellv1.RunCompleted:
		case cellv1.RunFailed:
			return end(cellv1.RunFailed, "rollback failed: "+rs.Status.Message)
		default:
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}
	if removing {
		return end(cellv1.RunFailed, cr.Status.Message+"; app "+app.Name+" was removed")
	}
	if appReadyAt(&app, app.Generation) && app.Spec.Version == app.Status.Version {
		return end(cellv1.RunRolledBack, cr.Status.Message+"; back on "+app.Status.Version)
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

// sameJSON compares two JSON documents by value.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return equality.Semantic.DeepEqual(x, y)
}

// appReadyAt reports whether the app passed its health check for the given
// spec generation (or a newer one).
func appReadyAt(app *cellv1.App, generation int64) bool {
	c := apimeta.FindStatusCondition(app.Status.Conditions, condReady)
	return app.Status.ObservedGeneration >= generation && c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration >= generation
}

// cancelBackup deletes the change's backup while it runs (deleting cancels
// it) and reports whether it still exists unfinished.
func (r *ChangeReconciler) cancelBackup(ctx context.Context, cr *cellv1.ChangeRequest) (bool, error) {
	if cr.Status.Backup == "" || cr.Status.Backup == "none" {
		return false, nil
	}
	var b cellv1.Backup
	if err := r.Get(ctx, client.ObjectKey{Name: cr.Status.Backup}, &b); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if b.Labels[labelRun] != string(cr.UID) || finished(b.Status.Phase) {
		return false, nil
	}
	if b.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, &b); client.IgnoreNotFound(err) != nil {
			return false, err
		}
	}
	return true, nil
}
