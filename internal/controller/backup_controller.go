package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
)

// BackupReconciler runs a Backup: database base backup first, then a restic
// snapshot of the volumes tagged with the database backup's coordinates.
type BackupReconciler struct {
	client.Client
	// Reader reads the backup gateway's lock past the cache.
	Reader      client.Reader
	ResticImage string
	Now         func() time.Time
}

func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&cellv1.Backup{}).
		Watches(&batchv1.Job{}, byRunLabel(labelBackup)).
		Complete(r)
}

func (r *BackupReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b cellv1.Backup
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	holder := "Backup/" + b.Name
	release := func() error {
		if err := releaseApp(ctx, r, b.Spec.App, holder); err != nil {
			return err
		}
		return releaseGateway(ctx, r, apiReader(r.Reader, r), "backup/"+b.Name)
	}
	// Deleting a running backup cancels it. It holds the app until its job
	// is stopped, so nothing keeps copying data a restore could replace.
	if !b.DeletionTimestamp.IsZero() {
		if b.Status.Phase == cellv1.RunRunning {
			if stopped, err := r.cancel(ctx, &b); err != nil || !stopped {
				return ctrl.Result{RequeueAfter: 2 * time.Second}, err
			}
		}
		if err := release(); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&b, backupFinalizer) {
			return ctrl.Result{}, r.Update(ctx, &b)
		}
		return ctrl.Result{}, nil
	}
	if b.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(&b, backupFinalizer) {
		return ctrl.Result{Requeue: true}, r.Update(ctx, &b)
	}
	if b.Status.Phase == cellv1.RunCompleted || b.Status.Phase == cellv1.RunFailed {
		return ctrl.Result{}, release()
	}
	end := func(phase cellv1.RunPhase, msg string) (ctrl.Result, error) {
		b.Status.Phase, b.Status.Message, b.Status.CompletedAt = phase, msg, ptr(metav1.NewTime(r.now()))
		if err := r.Status().Update(ctx, &b); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, release()
	}
	fail := func(msg string) (ctrl.Result, error) { return end(cellv1.RunFailed, msg) }
	target, err := loadTarget(ctx, r)
	if err != nil {
		return ctrl.Result{}, err
	}
	if target == nil {
		return fail("the cell has no backup target")
	}
	var app cellv1.App
	if err := r.Get(ctx, client.ObjectKey{Name: b.Spec.App}, &app); err != nil {
		if apierrors.IsNotFound(err) {
			return fail("app " + b.Spec.App + " does not exist")
		}
		return ctrl.Result{}, err
	}
	if b.Status.Phase == "" || b.Status.Phase == cellv1.RunPending {
		if !app.DeletionTimestamp.IsZero() {
			return fail("app " + app.Name + " is being removed")
		}
		// A backup does not run alongside a restore or change of the app:
		// it would copy data being replaced. A change's own backup runs
		// under that change's hold.
		// Nor while the target changes: it would write to two targets.
		if why, err := acquireGateway(ctx, r, apiReader(r.Reader, r), "backup/"+b.Name); err != nil || why != "" {
			if err == nil && (b.Status.Phase != cellv1.RunPending || b.Status.Message != why) {
				b.Status.Phase, b.Status.Message = cellv1.RunPending, why
				err = r.Status().Update(ctx, &b)
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
		ok, current, err := r.hold(ctx, &b, &app, holder)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ok {
			if b.Status.Phase != cellv1.RunPending || b.Status.Message != "waiting for "+current {
				b.Status.Phase, b.Status.Message = cellv1.RunPending, "waiting for "+current
				return ctrl.Result{RequeueAfter: 5 * time.Second}, r.Status().Update(ctx, &b)
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		b.Status.Phase, b.Status.StartedAt, b.Status.Message = cellv1.RunRunning, ptr(metav1.NewTime(r.now())), "database"
		return ctrl.Result{RequeueAfter: time.Second}, r.Status().Update(ctx, &b)
	}
	ns := Namespace(app.Name)

	if app.Spec.Needs.Database != nil && (b.Status.Database == nil || b.Status.Database.BackupID == "") {
		cluster := &unstructured.Unstructured{}
		cluster.SetGroupVersionKind(gvkCluster)
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: dbCluster}, cluster); err != nil {
			return ctrl.Result{}, err
		}
		sn := serverName(cluster)
		if sn == "" {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil // the App reconciler is enabling archiving
		}
		// The backup is bound to the cluster and WAL archive it was asked
		// of; a restore may replace the cluster before CloudNativePG runs it.
		pg := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1", "kind": "Backup",
			"metadata": map[string]any{"name": b.Name, "namespace": ns, "labels": toAny(labels(&app)),
				"annotations": map[string]any{annotationCluster: string(cluster.GetUID()), annotationServer: sn}},
			"spec": map[string]any{
				"cluster": map[string]any{"name": dbCluster}, "method": "plugin",
				"pluginConfiguration": map[string]any{"name": barmanPlugin},
			},
		}}
		if ok, err := ensureChild(ctx, r, pg, string(b.UID)); err != nil || !ok {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
		phase, _, _ := unstructured.NestedString(pg.Object, "status", "phase")
		switch phase {
		case "completed":
			id, _, _ := unstructured.NestedString(pg.Object, "status", "backupId")
			from, _, _ := unstructured.NestedString(pg.Object, "status", "pluginMetadata", "clusterUID")
			if a := pg.GetAnnotations(); from == "" || from != a[annotationCluster] || a[annotationServer] == "" {
				return fail("database backup " + id + " is not of the database it was asked of (replaced during the backup?)")
			}
			b.Status.Database = &cellv1.DatabaseBackup{ServerName: pg.GetAnnotations()[annotationServer], BackupID: id}
			b.Status.Message = "volumes"
			return ctrl.Result{RequeueAfter: time.Second}, r.Status().Update(ctx, &b)
		case "failed":
			msg, _, _ := unstructured.NestedString(pg.Object, "status", "error")
			return fail("database backup failed: " + msg)
		default:
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	claims, err := dataClaims(ctx, r, &app)
	if err != nil {
		return ctrl.Result{}, err
	}
	var dbServer, dbBackup string
	if d := b.Status.Database; d != nil {
		dbServer, dbBackup = d.ServerName, d.BackupID
	}
	job := resticJob(&app, target, r.ResticImage, jobName("backup", b.Name), backupScript, claims, true,
		corev1.EnvVar{Name: "BACKUP", Value: b.Name},
		corev1.EnvVar{Name: "DB_SERVER", Value: dbServer},
		corev1.EnvVar{Name: "DB_BACKUP", Value: dbBackup},
		corev1.EnvVar{Name: "CLAIMS", Value: strings.Join(claims, " ")})
	job.Labels = runLabels(job.Labels, labelBackup, b.Name)
	if ok, err := ensureChild(ctx, r, job, string(b.UID)); err != nil || !ok {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}
	done, out, err := jobResult(ctx, r, job)
	if err != nil {
		if done {
			return fail("volume backup: " + err.Error())
		}
		return ctrl.Result{}, err
	}
	if !done {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	snap, err := parseSnapshot(out)
	if err != nil {
		return fail("volume backup: " + err.Error())
	}
	// What the snapshot holds, not what the app has now.
	b.Status.VolumesSnapshot, b.Status.Claims = snap.ID, snap.tags("claim")
	b.Status.ClaimsRecorded = snap.tag("claims") == "recorded"
	return end(cellv1.RunCompleted, fmt.Sprintf("%d volumes", len(b.Status.Claims)))
}

// backupFinalizer keeps a running backup until it ended.
var backupFinalizer = cellv1.GroupVersion.Group + "/backup"

// Annotations binding a CloudNativePG backup to its cluster and archive.
var (
	annotationCluster = cellv1.GroupVersion.Group + "/cluster-uid"
	annotationServer  = cellv1.GroupVersion.Group + "/server-name"
)

// hold makes the backup the app's running operation, or confirms that it
// is the backup of the change holding the app.
func (r *BackupReconciler) hold(ctx context.Context, b *cellv1.Backup, app *cellv1.App, holder string) (bool, string, error) {
	if kind, name, _ := strings.Cut(app.Annotations[lockKey], "/"); kind == "ChangeRequest" {
		var cr cellv1.ChangeRequest
		err := r.Get(ctx, client.ObjectKey{Name: name}, &cr)
		if client.IgnoreNotFound(err) != nil {
			return false, "", err
		}
		if err == nil && cr.Status.Phase == cellv1.RunRunning && cr.Spec.App == app.Name &&
			cr.Status.Backup == b.Name && b.Labels[labelRun] == string(cr.UID) {
			return true, app.Annotations[lockKey], nil
		}
	}
	return acquireApp(ctx, r, app, holder)
}

// cancel stops a running backup: its database backup is dropped and its
// job deleted. It reports true once no pod of the job runs anymore.
func (r *BackupReconciler) cancel(ctx context.Context, b *cellv1.Backup) (bool, error) {
	ns := Namespace(b.Spec.App)
	pg := &unstructured.Unstructured{}
	pg.SetGroupVersionKind(gvkCNPGBackup)
	pg.SetNamespace(ns)
	pg.SetName(b.Name)
	if err := r.Delete(ctx, pg); client.IgnoreNotFound(err) != nil && !apimeta.IsNoMatchError(err) {
		return false, err
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: jobName("backup", b.Name)}}
	err := r.Get(ctx, client.ObjectKeyFromObject(job), job)
	if err == nil {
		// Foreground: the job goes only after its pods are gone.
		if job.DeletionTimestamp == nil {
			err = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground))
		}
		return false, client.IgnoreNotFound(err)
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{"job-name": job.Name}); err != nil {
		return false, err
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			return false, nil
		}
	}
	return true, nil
}
