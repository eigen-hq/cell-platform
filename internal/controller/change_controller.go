package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
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

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// changeFinalizer keeps a running change until it ended.
var changeFinalizer = cellv1.GroupVersion.Group + "/change"

// ChangeReconciler executes ChangeRequests, one per app at a time: wait for
// approval, back up, apply the patch, wait for the health check and roll
// back to the backup (spec and data) if it does not pass in time.
type ChangeReconciler struct {
	client.Client
	// Reader reads the backup gateway's lock past the cache.
	Reader client.Reader
	Now    func() time.Time
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
	// A change backs up before and may restore after: it holds the backup
	// gateway's target from start to end, rollback included.
	lockUser := "change/" + cr.Name
	release := func() error {
		if err := releaseApp(ctx, r, cr.Spec.App, holder); err != nil {
			return err
		}
		return releaseGateway(ctx, r, apiReader(r.Reader, r), lockUser)
	}
	// A started change (it holds the app) is not given up halfway: deleting
	// it waits until it ended. Waiting or ended changes go at once.
	if !cr.DeletionTimestamp.IsZero() && cr.Status.Phase != cellv1.RunRunning {
		if err := release(); err != nil {
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
		return ctrl.Result{}, release()
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
		return ctrl.Result{}, release()
	}
	wait := 5 * time.Second

	action := cr.Spec.Action
	if action == "" {
		action = actionUpdate
	}
	var app cellv1.App
	err := r.Get(ctx, client.ObjectKey{Name: cr.Spec.App}, &app)
	gone := apierrors.IsNotFound(err)
	if err != nil && !gone {
		return ctrl.Result{}, err
	}
	// A removal that deleted the app goes on without it: its data is next.
	removed := action == actionRemove && cr.Status.AppliedAt != nil
	if gone && action != actionInstall && !removed {
		return end(cellv1.RunFailed, "app "+cr.Spec.App+" does not exist")
	}

	removing := !gone && !app.DeletionTimestamp.IsZero()
	if cr.Status.Phase != cellv1.RunRunning {
		switch {
		case removing:
			return end(cellv1.RunFailed, "app "+app.Name+" is being removed")
		case cr.Spec.Approval.Decision == "Rejected":
			return end(cellv1.RunRejected, "rejected by "+cr.Spec.Approval.By)
		case cr.Spec.Approval.Required && cr.Spec.Approval.Decision != "Approved":
			return set(cellv1.RunWaitingForApproval, "waiting for approval", 0)
		}
		if action == actionInstall {
			return r.install(ctx, &cr, gone, &app, holder, set, end)
		}
		if why, err := acquireGateway(ctx, r, apiReader(r.Reader, r), lockUser); err != nil || why != "" {
			if err != nil {
				return ctrl.Result{}, err
			}
			return set(cellv1.RunPending, why, wait)
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
	if gone && action == actionInstall {
		// The cache may not show the app this change just created yet. A
		// deleted app keeps its finalizer until this change ends, so an app
		// that stays gone was forced out.
		if cr.Status.AppliedAt != nil && r.now().Sub(cr.Status.AppliedAt.Time) < time.Minute {
			return set(cellv1.RunRunning, "waiting for the health check", time.Second)
		}
		return end(cellv1.RunFailed, "app "+cr.Spec.App+" disappeared during the install")
	}
	if gone {
		return r.deleteData(ctx, &cr, set, end)
	}
	if cr.Status.AppUID != "" && cr.Status.AppUID != string(app.UID) {
		return end(cellv1.RunFailed, "app "+app.Name+" was removed and reinstalled while the change ran")
	}
	if removed && removing {
		// Removal waits for running operations: let go of the app.
		if err := releaseApp(ctx, r, app.Name, holder); err != nil {
			return ctrl.Result{}, err
		}
		return set(cellv1.RunRunning, "waiting for the app to be removed", wait)
	}
	// Removing the app ends the change; only a restore that is replacing
	// data runs to its end first.
	if removing && cr.Status.Restore == "" {
		return end(cellv1.RunFailed, "app "+app.Name+" was removed while the change ran")
	}
	if action == actionInstall {
		return r.installed(&cr, &app, set, end)
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

	switch action {
	case actionRemove:
		return r.removeApp(ctx, &cr, &app, set)
	case actionRestore:
		return r.restore(ctx, &cr, &app, removing, set, end)
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
		timeout := healthDeadline(&app, cr.Status.AppliedGeneration)
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

// Actions of a ChangeRequest.
const (
	actionUpdate  = "update"
	actionInstall = "install"
	actionRemove  = "remove"
	actionRestore = "restore"
)

type (
	setFunc func(phase cellv1.RunPhase, msg string, after time.Duration) (ctrl.Result, error)
	endFunc func(phase cellv1.RunPhase, msg string) (ctrl.Result, error)
)

// install creates the app with the change's spec, already held by the
// change, so nothing else starts on it before its first health check.
func (r *ChangeReconciler) install(ctx context.Context, cr *cellv1.ChangeRequest, gone bool, app *cellv1.App, holder string, set setFunc, end endFunc) (ctrl.Result, error) {
	if !gone && app.Annotations[lockKey] != holder {
		return end(cellv1.RunFailed, "app "+cr.Spec.App+" is already installed")
	}
	if gone {
		// A removal outlives its app (deleting the data comes last); a new
		// install of the name waits for it instead of adopting the data.
		var changes cellv1.ChangeRequestList
		if err := r.List(ctx, &changes); err != nil {
			return ctrl.Result{}, err
		}
		for _, other := range changes.Items {
			if other.Spec.App == cr.Spec.App && other.Spec.Action == actionRemove && other.Status.Phase == cellv1.RunRunning {
				return set(cellv1.RunPending, "waiting for ChangeRequest/"+other.Name+" to finish removing the app", 5*time.Second)
			}
		}
		if cr.Spec.Patch == nil {
			return end(cellv1.RunFailed, "no spec to install")
		}
		var spec cellv1.AppSpec
		if err := json.Unmarshal(cr.Spec.Patch.Raw, &spec); err != nil {
			return end(cellv1.RunFailed, "spec: "+err.Error())
		}
		*app = cellv1.App{
			ObjectMeta: metav1.ObjectMeta{Name: cr.Spec.App, Annotations: map[string]string{lockKey: holder}},
			Spec:       spec,
		}
		if err := r.Create(ctx, app); err != nil {
			if apierrors.IsInvalid(err) {
				return end(cellv1.RunFailed, "spec rejected: "+err.Error())
			}
			return ctrl.Result{}, err
		}
	}
	now := ptr(metav1.NewTime(r.now()))
	cr.Status.StartedAt, cr.Status.AppUID, cr.Status.Backup = now, string(app.UID), "none"
	cr.Status.AppliedGeneration, cr.Status.AppliedAt = app.Generation, now
	return set(cellv1.RunRunning, "waiting for the health check", 5*time.Second)
}

// installed ends an install with the app's first health check. An app that
// does not become healthy stays installed for diagnosis.
func (r *ChangeReconciler) installed(cr *cellv1.ChangeRequest, app *cellv1.App, set setFunc, end endFunc) (ctrl.Result, error) {
	if appReadyAt(app, cr.Status.AppliedGeneration) {
		return end(cellv1.RunSucceeded, "app installed and healthy")
	}
	timeout := healthDeadline(app, cr.Status.AppliedGeneration)
	if r.now().Sub(cr.Status.AppliedAt.Time) < timeout {
		return set(cellv1.RunRunning, "waiting for the health check", 5*time.Second)
	}
	reason := "no condition"
	if c := apimeta.FindStatusCondition(app.Status.Conditions, condReady); c != nil {
		reason = c.Message
	}
	return end(cellv1.RunFailed, fmt.Sprintf("unhealthy after %s (%s); the app stays installed for diagnosis", timeout, reason))
}

// removeApp deletes the app once its backup is done. The change then lets
// go of the app (see Reconcile); nothing new starts on an app that is being
// removed.
func (r *ChangeReconciler) removeApp(ctx context.Context, cr *cellv1.ChangeRequest, app *cellv1.App, set setFunc) (ctrl.Result, error) {
	if cr.Status.AppliedAt == nil {
		cr.Status.AppliedAt = ptr(metav1.NewTime(r.now()))
		return set(cellv1.RunRunning, "removing the app", time.Second)
	}
	if err := r.Delete(ctx, app, client.Preconditions{UID: &app.UID}); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// deleteData ends a removal: with DeleteData, the namespace the removed app
// left behind goes too (volumes, database, secrets).
func (r *ChangeReconciler) deleteData(ctx context.Context, cr *cellv1.ChangeRequest, set setFunc, end endFunc) (ctrl.Result, error) {
	if !cr.Spec.DeleteData {
		return end(cellv1.RunSucceeded, "app removed; its data stays in the cell")
	}
	var ns corev1.Namespace
	if err := r.Get(ctx, client.ObjectKey{Name: Namespace(cr.Spec.App)}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return end(cellv1.RunSucceeded, "app and its data removed; its backups stay")
		}
		return ctrl.Result{}, err
	}
	if !ns.DeletionTimestamp.IsZero() {
		return set(cellv1.RunRunning, "deleting the app's data", 5*time.Second)
	}
	if ns.Labels[cellv1.LabelRetained] != "true" {
		return end(cellv1.RunFailed, "app removed, but namespace "+ns.Name+" is not marked as retained data; it was kept")
	}
	// A reinstall by any path adopts the namespace by applying it without
	// the retained label, which changes its resourceVersion: the delete
	// then conflicts and the next pass finds the app back and keeps the data.
	if err := r.Delete(ctx, &ns, client.Preconditions{UID: &ns.UID, ResourceVersion: &ns.ResourceVersion}); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	return set(cellv1.RunRunning, "deleting the app's data", 5*time.Second)
}

// restore brings back a backup's data under the change's hold, after the
// change's own backup of the current state.
func (r *ChangeReconciler) restore(ctx context.Context, cr *cellv1.ChangeRequest, app *cellv1.App, removing bool, set setFunc, end endFunc) (ctrl.Result, error) {
	if cr.Status.Restore == "" {
		cr.Status.Restore = cr.Name + "-restore"
		return set(cellv1.RunRunning, "restoring backup "+cr.Spec.Backup, time.Second)
	}
	rs := &cellv1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Status.Restore, Labels: runLabels(map[string]string{cellv1.LabelApp: app.Name}, labelChange, cr.Name)},
		Spec:       cellv1.RestoreSpec{App: app.Name, Backup: cr.Spec.Backup, Change: cr.Name},
	}
	if ok, err := ensureChild(ctx, r, rs, string(cr.UID)); err != nil || !ok {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}
	switch rs.Status.Phase {
	case cellv1.RunCompleted:
	case cellv1.RunFailed:
		return end(cellv1.RunFailed, "restore failed: "+rs.Status.Message)
	default:
		return set(cellv1.RunRunning, "restoring backup "+cr.Spec.Backup, 5*time.Second)
	}
	if removing {
		return end(cellv1.RunFailed, "backup "+cr.Spec.Backup+" restored, but app "+app.Name+" was removed")
	}
	if appReadyAt(app, app.Generation) {
		return end(cellv1.RunSucceeded, "backup "+cr.Spec.Backup+" restored, app healthy")
	}
	timeout := healthDeadline(app, app.Generation)
	if rs.Status.CompletedAt != nil && r.now().Sub(rs.Status.CompletedAt.Time) > timeout {
		reason := "no condition"
		if c := apimeta.FindStatusCondition(app.Status.Conditions, condReady); c != nil {
			reason = c.Message
		}
		return end(cellv1.RunFailed, fmt.Sprintf("backup %s restored, but the app is not healthy after %s (%s); backup %s holds the state before the restore", cr.Spec.Backup, timeout, reason, cr.Status.Backup))
	}
	return set(cellv1.RunRunning, "backup restored, waiting for the health check", 5*time.Second)
}
