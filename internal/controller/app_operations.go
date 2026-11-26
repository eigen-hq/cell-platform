package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// Labels linking objects to the run that created them: Backups and
// Restores of a ChangeRequest, Jobs of a Backup or Restore.
var (
	labelChange  = cellv1.GroupVersion.Group + "/change"
	labelBackup  = cellv1.GroupVersion.Group + "/backup"
	labelRestore = cellv1.GroupVersion.Group + "/restore"
)

// Operations (changes, restores and backups) on one app run one at a time. The app
// carries the running operation in an annotation, written with optimistic
// locking, so taking it is atomic across controllers and restarts.
var lockKey = cellv1.GroupVersion.Group + "/operation"

// acquireApp makes holder ("ChangeRequest/<name>", "Restore/<name>" or
// "Backup/<name>") the app's running operation. If another operation holds
// the app, it returns false and names it. A holder that finished or no longer exists is taken
// over. A conflict (someone else wrote first) also returns false.
func acquireApp(ctx context.Context, c client.Client, app *cellv1.App, holder string) (bool, string, error) {
	current := app.Annotations[lockKey]
	if current == holder {
		return true, holder, nil
	}
	if current != "" {
		active, err := operationActive(ctx, c, current)
		if err != nil || active {
			return false, current, err
		}
	}
	return setLock(ctx, c, app, holder)
}

// releaseApp gives the app free if holder holds it.
func releaseApp(ctx context.Context, c client.Client, appName, holder string) error {
	var app cellv1.App
	if err := c.Get(ctx, client.ObjectKey{Name: appName}, &app); err != nil {
		return client.IgnoreNotFound(err)
	}
	if app.Annotations[lockKey] != holder {
		return nil
	}
	ok, _, err := setLock(ctx, c, &app, "")
	if err == nil && !ok {
		return fmt.Errorf("app %s changed while releasing it, retrying", appName)
	}
	return err
}

func setLock(ctx context.Context, c client.Client, app *cellv1.App, holder string) (bool, string, error) {
	base := app.DeepCopy()
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	if holder == "" {
		delete(app.Annotations, lockKey)
	} else {
		app.Annotations[lockKey] = holder
	}
	err := c.Patch(ctx, app, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		return false, "a concurrent write", nil
	}
	return err == nil, holder, err
}

func operationActive(ctx context.Context, c client.Client, holder string) (bool, error) {
	kind, name, _ := strings.Cut(holder, "/")
	var obj client.Object
	var phase func() cellv1.RunPhase
	switch kind {
	case "ChangeRequest":
		cr := &cellv1.ChangeRequest{}
		obj, phase = cr, func() cellv1.RunPhase { return cr.Status.Phase }
	case "Restore":
		rs := &cellv1.Restore{}
		obj, phase = rs, func() cellv1.RunPhase { return rs.Status.Phase }
	case "Backup":
		b := &cellv1.Backup{}
		obj, phase = b, func() cellv1.RunPhase { return b.Status.Phase }
	default:
		return false, nil
	}
	if err := c.Get(ctx, client.ObjectKey{Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return !finished(phase()), nil
}

// runLabels returns labels with key set to the run's name. Names longer
// than a label value allows are left out; those runs rely on requeues.
func runLabels(labels map[string]string, key, run string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		out[k] = v
	}
	if len(run) <= 63 {
		out[key] = run
	}
	return out
}

// byRunLabel maps an object to the cluster-scoped run named in a label.
func byRunLabel(label string) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if name := o.GetLabels()[label]; name != "" {
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: name}}}
		}
		return nil
	})
}

// backupRetention is how long backups are kept (also in the bucket).
const backupRetention = 30 * 24 * time.Hour

// activity is what Restores and ChangeRequests currently do to an app.
type activity struct {
	phase        cellv1.Phase // set while a change or restore runs
	paused       bool         // a restore owns the app's workloads and data
	pausedBy     string       // the running restore, if any
	restoring    bool         // a restore waits for the app to be configured again
	reason       string       // why the app is paused
	failedChange string       // set if the newest finished change failed
	restarted    string       // UID of the running restart whose new pods rolled out
}

func finished(p cellv1.RunPhase) bool {
	switch p {
	case cellv1.RunCompleted, cellv1.RunFailed, cellv1.RunSucceeded, cellv1.RunRolledBack, cellv1.RunRejected:
		return true
	}
	return false
}

func (r *AppReconciler) activity(ctx context.Context, app *cellv1.App) (activity, error) {
	var a activity
	var changes cellv1.ChangeRequestList
	if err := r.List(ctx, &changes); err != nil {
		return a, err
	}
	// Restores asked for by a change are rollbacks, unless the change's
	// action is to restore.
	restoreChange := map[string]bool{}
	for _, cr := range changes.Items {
		restoreChange[cr.Name] = cr.Spec.Action == "restore"
	}
	var restores cellv1.RestoreList
	if err := r.List(ctx, &restores); err != nil {
		return a, err
	}
	var brokenBy, lastGood *cellv1.Restore
	for i := range restores.Items {
		rs := &restores.Items[i]
		if rs.Spec.App != app.Name || rs.CreationTimestamp.Before(&app.CreationTimestamp) {
			continue
		}
		// Order by when restores ended, not when they were asked for.
		destructive := rs.Status.Step == stepVolumes || rs.Status.Step == stepDatabase
		if rs.Status.Phase == cellv1.RunFailed && destructive && rs.Status.CompletedAt != nil &&
			(brokenBy == nil || rs.Status.CompletedAt.After(brokenBy.Status.CompletedAt.Time)) {
			brokenBy = rs
		}
		if rs.Status.Phase == cellv1.RunCompleted && rs.Status.CompletedAt != nil &&
			(lastGood == nil || rs.Status.CompletedAt.After(lastGood.Status.CompletedAt.Time)) {
			lastGood = rs
		}
		// Only a started restore holds the app; a new or waiting one does not.
		if rs.Status.Phase != cellv1.RunRunning {
			continue
		}
		a.phase = cellv1.PhaseRestoring
		if rs.Spec.Change != "" && !restoreChange[rs.Spec.Change] {
			a.phase = cellv1.PhaseRollingBack
		}
		// Once the data is back, the app is configured for its current spec
		// again before the restore lets its workloads run.
		if rs.Status.Step == stepStart {
			a.restoring = true
			continue
		}
		a.paused, a.reason, a.pausedBy = true, "restore "+rs.Name+" is replacing the app's data", rs.Name
	}
	// A restore that failed while replacing data leaves volumes and database
	// in between: the app stays stopped until a restore succeeds or someone
	// deletes the failed one.
	if rs := brokenBy; !a.paused && !a.restoring && rs != nil && (lastGood == nil || !lastGood.Status.CompletedAt.After(rs.Status.CompletedAt.Time)) {
		a.paused, a.phase = true, cellv1.PhaseNeedsAttention
		a.reason = fmt.Sprintf("restore %s failed while replacing data (%s); the app stays stopped until a restore succeeds or %s is deleted",
			rs.Name, rs.Status.Message, rs.Name)
	}
	var latest *cellv1.ChangeRequest
	for i := range changes.Items {
		cr := &changes.Items[i]
		// Changes to an earlier installation of the same name are history.
		if cr.Spec.App != app.Name || cr.CreationTimestamp.Before(&app.CreationTimestamp) {
			continue
		}
		switch {
		case cr.Status.Phase == cellv1.RunRunning && a.phase == "":
			if cr.Spec.Action == "restart" && cr.Status.RolledOutAt != nil {
				a.restarted = string(cr.UID)
			}
			switch {
			case cr.Spec.Action == "restore":
				a.phase = cellv1.PhaseRestoring
			case cr.Status.Restore != "":
				a.phase = cellv1.PhaseRollingBack
			default:
				a.phase = cellv1.PhaseUpdating
			}
		// Changes that touched the app: applied a spec or restored data.
		case finished(cr.Status.Phase) && cr.Status.FinishedAt != nil && (cr.Status.AppliedGeneration > 0 || cr.Status.Restore != ""):
			if latest == nil || cr.Status.FinishedAt.After(latest.Status.FinishedAt.Time) {
				latest = cr
			}
		}
	}
	if latest != nil && latest.Status.Phase != cellv1.RunSucceeded {
		a.failedChange = fmt.Sprintf("change %s: %s (%s)", latest.Name, latest.Status.Phase, latest.Status.Message)
	}
	return a, nil
}

// ForgetLogin makes the next reconcile write the app's login client again,
// e.g. after a restore brought credentials from a backup.
func (r *AppReconciler) ForgetLogin(app string) { r.loginSynced.Delete(app) }

func hasData(app *cellv1.App) bool {
	return app.Spec.Needs.Database != nil || len(app.Spec.Needs.Volumes) > 0 || app.Spec.Source.Helm != nil
}

func backupInterval(schedule string) time.Duration {
	switch schedule {
	case "hourly":
		return time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	case "none":
		return 0
	}
	return 24 * time.Hour // daily, the default
}

// scheduleBackup records the newest completed backup, removes backups past
// retention, and starts a backup when the schedule says one is due (the
// first one right after installation).
func (r *AppReconciler) scheduleBackup(ctx context.Context, app *cellv1.App, target *backupTarget) error {
	var backups cellv1.BackupList
	if err := r.List(ctx, &backups); err != nil {
		return err
	}
	now := r.now()
	var newest, lastDone *cellv1.Backup
	// A database still archived plain (before E-67) is backed up at once:
	// that backup moves it to an encrypted archive. Only while every backup
	// is a completed plain one, so once: the backup it starts is none.
	plain := false
	for i := range backups.Items {
		b := &backups.Items[i]
		if b.Spec.App != app.Name {
			continue
		}
		if b.Status.CompletedAt != nil && now.Sub(b.Status.CompletedAt.Time) > backupRetention {
			if err := r.Delete(ctx, b); client.IgnoreNotFound(err) != nil {
				return err
			}
			continue
		}
		inPlain := b.Status.Phase == cellv1.RunCompleted && b.Status.Database != nil &&
			!strings.HasPrefix(b.Status.Database.ServerName, cryptArchive)
		plain = inPlain && (newest == nil || plain)
		if newest == nil || b.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = b
		}
		if b.Status.Phase == cellv1.RunCompleted && (lastDone == nil || b.Status.CompletedAt.After(lastDone.Status.CompletedAt.Time)) {
			lastDone = b
		}
	}
	if lastDone != nil {
		app.Status.LastBackup = &cellv1.LastBackup{Name: lastDone.Name, At: *lastDone.Status.CompletedAt}
	}
	interval := backupInterval(app.Spec.Backup.Schedule)
	if target == nil || !hasData(app) || interval == 0 {
		return nil
	}
	if newest != nil && now.Sub(newest.CreationTimestamp.Time) < interval && !plain {
		return nil
	}
	return createOnce(ctx, r, &cellv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:   app.Name + "-" + now.UTC().Format("20060102-150405"),
			Labels: map[string]string{cellv1.LabelApp: app.Name},
		},
		Spec: cellv1.BackupSpec{App: app.Name, Reason: "scheduled"},
	})
}
