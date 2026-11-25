package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
)

// Restore steps, in order.
const (
	stepLookup   = "lookup"
	stepStop     = "stop"
	stepVolumes  = "volumes"
	stepDatabase = "database"
	stepStart    = "start"
)

// restoreFinalizer keeps a running restore until it ended.
var restoreFinalizer = cellv1.GroupVersion.Group + "/restore"

// restoreAnnotation marks the database cluster a restore created (by the
// restore's UID: names repeat).
var restoreAnnotation = cellv1.GroupVersion.Group + "/restore"

// RestoreReconciler brings an app back to a backup. Until the data is back,
// the App reconciler leaves the app alone.
type RestoreReconciler struct {
	client.Client
	// Reader reads the backup gateway's lock past the cache.
	Reader      client.Reader
	ResticImage string
	Now         func() time.Time
	// Restored is called when an app's data was replaced (its login client
	// must be written again, its credentials may come from another cell).
	Restored func(app string)
}

func (r *RestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&cellv1.Restore{}).
		Watches(&batchv1.Job{}, byRunLabel(labelRestore)).
		Complete(r)
}

func (r *RestoreReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rs cellv1.Restore
	if err := r.Get(ctx, req.NamespacedName, &rs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// A rollback runs under its change's hold on the app; it is released by
	// the change. Other restores hold the app themselves.
	holder := "Restore/" + rs.Name
	if rs.Spec.Change != "" {
		holder = "ChangeRequest/" + rs.Spec.Change
	}
	release := func() error {
		if err := releaseGateway(ctx, r, apiReader(r.Reader, r), "restore/"+rs.Name); err != nil || rs.Spec.Change != "" {
			return err
		}
		return releaseApp(ctx, r, rs.Spec.App, holder)
	}
	// A started restore is not given up halfway: deleting it waits until it
	// ended, the app stays paused meanwhile. Pending or ended restores go.
	if !rs.DeletionTimestamp.IsZero() && rs.Status.Phase != cellv1.RunRunning {
		if err := release(); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&rs, restoreFinalizer) {
			return ctrl.Result{}, r.Update(ctx, &rs)
		}
		return ctrl.Result{}, nil
	}
	if rs.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(&rs, restoreFinalizer) {
		return ctrl.Result{Requeue: true}, r.Update(ctx, &rs)
	}
	if rs.Status.Phase == cellv1.RunCompleted || rs.Status.Phase == cellv1.RunFailed {
		return ctrl.Result{}, release()
	}
	fail := func(msg string) (ctrl.Result, error) {
		rs.Status.Phase, rs.Status.Message, rs.Status.CompletedAt = cellv1.RunFailed, msg, ptr(metav1.NewTime(r.now()))
		if err := r.Status().Update(ctx, &rs); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, release()
	}
	next := func(step, msg string) (ctrl.Result, error) {
		rs.Status.Step, rs.Status.Message = step, msg
		return ctrl.Result{RequeueAfter: time.Second}, r.Status().Update(ctx, &rs)
	}
	wait := ctrl.Result{RequeueAfter: 5 * time.Second}

	target, err := loadTarget(ctx, r)
	if err != nil {
		return ctrl.Result{}, err
	}
	if target == nil {
		return fail("the cell has no backup target")
	}
	var app cellv1.App
	if err := r.Get(ctx, client.ObjectKey{Name: rs.Spec.App}, &app); err != nil {
		if apierrors.IsNotFound(err) {
			return fail("app " + rs.Spec.App + " does not exist; install it first")
		}
		return ctrl.Result{}, err
	}
	removing := !app.DeletionTimestamp.IsZero()
	if rs.Status.Phase == "" || rs.Status.Phase == cellv1.RunPending {
		if removing {
			return fail("app " + app.Name + " is being removed")
		}
		// Until the app reached its workload once, its namespace, backup
		// credentials and volumes may not exist yet, and a running restore
		// would keep the app from creating them.
		if apimeta.FindStatusCondition(app.Status.Conditions, condWorkload) == nil {
			if rs.Status.Phase != cellv1.RunPending || rs.Status.Message != "waiting for the app to be set up" {
				rs.Status.Phase, rs.Status.Message = cellv1.RunPending, "waiting for the app to be set up"
				return wait, r.Status().Update(ctx, &rs)
			}
			return wait, nil
		}
		if !hasData(&app) {
			return fail("app " + app.Name + " has no data to restore")
		}
		// The app writes its backup credentials; a restore that paused it
		// before they match the target would wait for them forever.
		if ok, err := backupReady(ctx, r, &app, target); err != nil || !ok {
			if err == nil && (rs.Status.Phase != cellv1.RunPending || rs.Status.Message != "waiting for the app's backup credentials") {
				rs.Status.Phase, rs.Status.Message = cellv1.RunPending, "waiting for the app's backup credentials"
				err = r.Status().Update(ctx, &rs)
			}
			return wait, err
		}
		if rs.Spec.Change != "" {
			var parent cellv1.ChangeRequest
			err := r.Get(ctx, client.ObjectKey{Name: rs.Spec.Change}, &parent)
			if err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			if err != nil || parent.Spec.App != app.Name || parent.Status.Phase != cellv1.RunRunning ||
				parent.Status.Restore != rs.Name || rs.Labels[labelRun] != string(parent.UID) {
				return fail("not a rollback of change " + rs.Spec.Change + ": that change is not running on " + app.Name + " or names another restore")
			}
		}
		// Not while the target changes: the restore would read from one and
		// the app's next backup write to the other.
		if why, err := acquireGateway(ctx, r, apiReader(r.Reader, r), "restore/"+rs.Name); err != nil || why != "" {
			if err == nil && (rs.Status.Phase != cellv1.RunPending || rs.Status.Message != why) {
				rs.Status.Phase, rs.Status.Message = cellv1.RunPending, why
				err = r.Status().Update(ctx, &rs)
			}
			return wait, err
		}
		ok, current, err := acquireApp(ctx, r, &app, holder)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ok {
			if rs.Status.Phase != cellv1.RunPending || rs.Status.Message != "waiting for "+current {
				rs.Status.Phase, rs.Status.Message = cellv1.RunPending, "waiting for "+current
				return wait, r.Status().Update(ctx, &rs)
			}
			return wait, nil
		}
		rs.Status.Phase, rs.Status.StartedAt, rs.Status.AppUID = cellv1.RunRunning, ptr(metav1.NewTime(r.now())), string(app.UID)
		return next(stepLookup, "finding the backup")
	}
	if rs.Status.AppUID != string(app.UID) {
		return fail("app " + app.Name + " was removed and reinstalled while the restore ran")
	}
	// Removing the app waits for a restore that is replacing data; one that
	// has not touched data yet gives way.
	if removing && (rs.Status.Step == stepLookup || rs.Status.Step == stepStop) {
		return fail("app " + app.Name + " is being removed")
	}
	ns := Namespace(app.Name)

	switch rs.Status.Step {
	case stepLookup:
		resolved, done, err := r.lookup(ctx, &rs, &app, target)
		if err != nil {
			if done {
				return fail(err.Error())
			}
			return ctrl.Result{}, err
		}
		if !done {
			return wait, nil
		}
		if app.Spec.Needs.Database != nil && (resolved.Database == nil || resolved.Database.BackupID == "") {
			return fail("backup " + resolved.Backup + " has no database backup")
		}
		// Checked before the app is stopped, so it fails without touching it.
		present, err := dataClaims(ctx, r, &app)
		if err != nil {
			return ctrl.Result{}, err
		}
		if c := missingClaim(resolved, present); c != "" {
			return fail("volume " + c + " of the backup does not exist in the app")
		}
		rs.Status.Resolved = resolved
		return next(stepStop, "stopping the app")

	case stepStop:
		stopped, err := r.stop(ctx, &rs, ns)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !stopped {
			return wait, r.Status().Update(ctx, &rs)
		}
		return next(stepVolumes, "restoring volumes")

	case stepVolumes:
		present, err := dataClaims(ctx, r, &app)
		if err != nil {
			return ctrl.Result{}, err
		}
		// Restore exactly the volumes in the backup. Volumes added later (by
		// a change being rolled back) stay as they are; older snapshots that
		// did not record their volumes cover all of them.
		claims := rs.Status.Resolved.Claims
		if !rs.Status.Resolved.ClaimsRecorded {
			claims = present
		}
		if c := missingClaim(rs.Status.Resolved, present); c != "" {
			return fail("volume " + c + " of the backup does not exist in the app")
		}
		if len(claims) > 0 {
			job := resticJob(&app, target, r.ResticImage, jobName("restore", rs.Name), restoreScript, claims, false,
				corev1.EnvVar{Name: "SNAPSHOT", Value: rs.Status.Resolved.VolumesSnapshot},
				corev1.EnvVar{Name: "CLAIMS", Value: strings.Join(claims, " ")})
			job.Labels = runLabels(job.Labels, labelRestore, rs.Name)
			if ok, err := ensureChild(ctx, r, job, string(rs.UID)); err != nil || !ok {
				return wait, err
			}
			done, _, err := jobResult(ctx, r, job)
			if err != nil {
				if done {
					return fail("volumes: " + err.Error())
				}
				return ctrl.Result{}, err
			}
			if !done {
				return wait, nil
			}
		}
		return next(stepDatabase, "restoring the database")

	case stepDatabase:
		if app.Spec.Needs.Database != nil {
			ready, msg, err := r.recoverDatabase(ctx, &rs, &app)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !ready {
				if msg != rs.Status.Message {
					rs.Status.Message = msg
					return wait, r.Status().Update(ctx, &rs)
				}
				return wait, nil
			}
		}
		if r.Restored != nil {
			r.Restored(app.Name)
		}
		return next(stepStart, "waiting for the app to be configured for its spec")

	case stepStart:
		// The App reconciler takes the app back and configures its workloads
		// for the app's current spec (for a rollback: the previous one), the
		// Helm release included, before anything runs again: nothing starts
		// with the configuration being rolled back on the restored data.
		// An app being removed is not started again; it keeps the data.
		rs.Status.Message = "restored from " + rs.Status.Resolved.Backup
		if removing {
			rs.Status.Message += "; the app was removed before it started again"
		} else {
			if app.Status.PausedBy != "" || app.Status.WorkloadGeneration < app.Generation {
				msg, err := releaseGaveUp(ctx, r, ns)
				if err != nil || msg == "" {
					return wait, err
				}
				return fail("Flux gave up deploying the app's spec (" + msg + "); the app was not started")
			}
			if err := r.start(ctx, &rs, ns); err != nil {
				return ctrl.Result{}, err
			}
		}
		rs.Status.Phase, rs.Status.CompletedAt = cellv1.RunCompleted, ptr(metav1.NewTime(r.now()))
		if err := r.Status().Update(ctx, &rs); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, release()
	}
	return fail("unknown step " + rs.Status.Step)
}

// lookup resolves the backup: a completed Backup of this cell, or the
// newest matching restic snapshot in the bucket (also from other cells).
func (r *RestoreReconciler) lookup(ctx context.Context, rs *cellv1.Restore, app *cellv1.App, target *backupTarget) (*cellv1.ResolvedBackup, bool, error) {
	name := rs.Spec.Backup
	if name == "" {
		name = "latest"
	}
	if name != "latest" {
		var b cellv1.Backup
		err := r.Get(ctx, client.ObjectKey{Name: name}, &b)
		if err == nil && b.Spec.App == app.Name && b.Status.Phase == cellv1.RunCompleted {
			return &cellv1.ResolvedBackup{Backup: b.Name, VolumesSnapshot: b.Status.VolumesSnapshot, Database: b.Status.Database,
				Claims: b.Status.Claims, ClaimsRecorded: b.Status.ClaimsRecorded}, true, nil
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, false, err
		}
	}
	job := resticJob(app, target, r.ResticImage, jobName("lookup", rs.Name), lookupScript, nil, true,
		corev1.EnvVar{Name: "BACKUP", Value: name})
	job.Labels = runLabels(job.Labels, labelRestore, rs.Name)
	if ok, err := ensureChild(ctx, r, job, string(rs.UID)); err != nil || !ok {
		return nil, false, err
	}
	done, out, err := jobResult(ctx, r, job)
	if err != nil || !done {
		return nil, done, err
	}
	snap, err := parseSnapshot(out)
	if err != nil {
		return nil, true, fmt.Errorf("backup %q of %s: %w", name, app.Name, err)
	}
	resolved := &cellv1.ResolvedBackup{Backup: snap.tag("backup"), VolumesSnapshot: snap.ID,
		Claims: snap.tags("claim"), ClaimsRecorded: snap.tag("claims") == "recorded"}
	if sn := snap.tag("db-server"); sn != "" {
		resolved.Database = &cellv1.DatabaseBackup{ServerName: sn, BackupID: snap.tag("db-backup")}
	}
	return resolved, true, nil
}

// stop scales every workload of the app to zero (remembering the replica
// counts) and suspends its Helm release, then waits for the pods to go.
func (r *RestoreReconciler) stop(ctx context.Context, rs *cellv1.Restore, ns string) (bool, error) {
	// The App reconciler confirms it stopped managing the app for this
	// restore; no reconcile from before can re-apply the workload after.
	var app cellv1.App
	if err := r.Get(ctx, client.ObjectKey{Name: rs.Spec.App}, &app); err != nil {
		return false, err
	}
	if app.Status.PausedBy != rs.Name {
		rs.Status.Message = "waiting for the operator to hand the app over"
		return false, nil
	}
	paused, err := r.suspendRelease(ctx, ns)
	if err != nil {
		return false, err
	}
	if !paused {
		rs.Status.Message = "waiting for Flux to pause the Helm release"
		return false, nil
	}
	if rs.Status.Replicas == nil {
		rs.Status.Replicas = map[string]int32{}
		var deploys appsv1.DeploymentList
		if err := r.List(ctx, &deploys, client.InNamespace(ns)); err != nil {
			return false, err
		}
		for _, d := range deploys.Items {
			rs.Status.Replicas["Deployment/"+d.Name] = replicas(d.Spec.Replicas)
		}
		var sets appsv1.StatefulSetList
		if err := r.List(ctx, &sets, client.InNamespace(ns)); err != nil {
			return false, err
		}
		for _, s := range sets.Items {
			rs.Status.Replicas["StatefulSet/"+s.Name] = replicas(s.Spec.Replicas)
		}
		// CronJobs: 1 if it was active and must be resumed afterwards.
		var crons batchv1.CronJobList
		if err := r.List(ctx, &crons, client.InNamespace(ns)); err != nil {
			return false, err
		}
		for _, c := range crons.Items {
			rs.Status.Replicas["CronJob/"+c.Name] = 0
			if c.Spec.Suspend == nil || !*c.Spec.Suspend {
				rs.Status.Replicas["CronJob/"+c.Name] = 1
			}
		}
		if err := r.Status().Update(ctx, rs); err != nil {
			return false, err
		}
	}
	if err := r.scale(ctx, ns, rs.Status.Replicas, true, nil); err != nil {
		return false, err
	}
	// The app's own Jobs (from its chart or CronJobs) may write to the
	// volumes too; they finish first. The operator's Jobs carry a run label.
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(ns)); err != nil {
		return false, err
	}
	ours := map[string]bool{}
	for _, j := range jobs.Items {
		if j.Labels[labelRun] != "" {
			ours[j.Name] = true
			continue
		}
		if !jobFinished(&j) {
			rs.Status.Message = "waiting for job " + j.Name + " to finish"
			return false, nil
		}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(ns)); err != nil {
		return false, err
	}
	for _, p := range pods.Items {
		_, isDB := p.Labels["cnpg.io/cluster"]
		done := p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
		if !isDB && !done && !ours[p.Labels["job-name"]] {
			rs.Status.Message = "waiting for " + p.Name + " to stop"
			return false, nil
		}
	}
	return true, nil
}

func jobFinished(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
			return true
		}
	}
	return false
}

// start sets back what the restore stopped. Helm releases themselves are
// resumed by the App reconciler, together with the spec they are to deploy.
func (r *RestoreReconciler) start(ctx context.Context, rs *cellv1.Restore, ns string) error {
	chart, err := chartSettings(ctx, r, ns)
	if err != nil {
		return err
	}
	return r.scale(ctx, ns, rs.Status.Replicas, false, chart)
}

// restoreFieldManager owns the fields a restore sets to stop the app.
const restoreFieldManager = "cell-restore"

// scale stops workloads (zero replicas, CronJobs suspended) or sets them
// back. For a rollback, what was recorded comes from the version rolled
// back: a workload of the deployed chart gets what the chart sets, any
// other gets the recorded value unless someone else (the App reconciler)
// set the field since the stop.
func (r *RestoreReconciler) scale(ctx context.Context, ns string, recorded map[string]int32, stop bool, chart map[string]string) error {
	for key, n := range recorded {
		kind, name := splitKey(key)
		var obj client.Object
		field, value := "replicas", fmt.Sprint(n)
		switch kind {
		case "CronJob":
			obj, field, value = &batchv1.CronJob{}, "suspend", fmt.Sprint(n == 0)
		case "Deployment":
			obj = &appsv1.Deployment{}
		case "StatefulSet":
			obj = &appsv1.StatefulSet{}
		default:
			return fmt.Errorf("unknown workload %s", key)
		}
		if stop {
			value = "0"
			if kind == "CronJob" {
				value = "true"
			}
		}
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if v, ok := chart[key]; ok && !stop {
			value = v
		} else if !stop && !ownsField(obj, restoreFieldManager, "f:spec", "f:"+field) {
			continue
		}
		patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{%q:%s}}`, field, value)))
		if err := r.Patch(ctx, obj, patch, client.FieldOwner(restoreFieldManager)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// ownsField reports whether manager owns the field at path.
func ownsField(obj client.Object, manager string, path ...string) bool {
	for _, mf := range obj.GetManagedFields() {
		if mf.Manager != manager || mf.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		if json.Unmarshal(mf.FieldsV1.Raw, &fields) != nil {
			continue
		}
		for i, p := range path {
			next, ok := fields[p].(map[string]any)
			if !ok {
				break
			}
			if i == len(path)-1 {
				return true
			}
			fields = next
		}
	}
	return false
}

// releaseGaveUp returns why Flux stopped trying to deploy a Helm release of
// the app's current spec (Stalled, e.g. retries exceeded), or "".
func releaseGaveUp(ctx context.Context, c client.Reader, ns string) (string, error) {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(gvkHelmRelease.GroupVersion().WithKind("HelmReleaseList"))
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return "", err
	}
	for _, hr := range list.Items {
		suspended, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend")
		observed, _, _ := unstructured.NestedInt64(hr.Object, "status", "observedGeneration")
		if suspended || observed < hr.GetGeneration() {
			continue
		}
		conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
		for _, raw := range conds {
			if c, _ := raw.(map[string]any); c["type"] == "Stalled" && c["status"] == "True" {
				return fmt.Sprint(c["message"]), nil
			}
		}
	}
	return "", nil
}

// suspendRelease suspends the app's Helm releases and reports whether Flux
// has seen it. Suspending does not stop a reconciliation that
// is already running (it may still upgrade the chart and bring workloads
// back); Flux records the generation it handled when that reconciliation
// ended, also for suspended releases, so a release whose observed
// generation reached the suspended spec has no reconciliation in flight.
func (r *RestoreReconciler) suspendRelease(ctx context.Context, ns string) (bool, error) {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(gvkHelmRelease.GroupVersion().WithKind("HelmReleaseList"))
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return false, err
	}
	observed := true
	for i := range list.Items {
		hr := &list.Items[i]
		if err := r.Patch(ctx, hr, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"suspend":true}}`))); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if gen, _, _ := unstructured.NestedInt64(hr.Object, "status", "observedGeneration"); gen < hr.GetGeneration() {
			observed = false
		}
	}
	return observed, nil
}

// recoverDatabase replaces the app's database cluster with one recovered
// from the backup, archiving into a new WAL archive.
func (r *RestoreReconciler) recoverDatabase(ctx context.Context, rs *cellv1.Restore, app *cellv1.App) (bool, string, error) {
	ns := Namespace(app.Name)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(gvkCluster)
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: dbCluster}, cluster)
	switch {
	case err == nil && cluster.GetAnnotations()[restoreAnnotation] == string(rs.UID):
		return readyCondition(cluster)
	case err == nil:
		if cluster.GetDeletionTimestamp() == nil {
			if err := r.Delete(ctx, cluster); client.IgnoreNotFound(err) != nil {
				return false, "", err
			}
		}
		return false, "removing the current database", nil
	case !apierrors.IsNotFound(err):
		return false, "", err
	}
	// The old cluster's volumes must be gone before the new one claims them.
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs, client.InNamespace(ns), client.MatchingLabels{"cnpg.io/cluster": dbCluster}); err != nil {
		return false, "", err
	}
	if len(pvcs.Items) > 0 {
		return false, "waiting for the old database volumes to go", nil
	}
	if err := r.Create(ctx, recoveryDatabase(app, *rs.Status.Resolved.Database, newServerName(r.now()), string(rs.UID))); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, "", err
	}
	return false, "recovering the database", nil
}

// missingClaim names a volume of the backup that the app does not have.
func missingClaim(b *cellv1.ResolvedBackup, present []string) string {
	if !b.ClaimsRecorded {
		return ""
	}
	for _, c := range b.Claims {
		if !slices.Contains(present, c) {
			return c
		}
	}
	return ""
}

func replicas(n *int32) int32 {
	if n == nil {
		return 1
	}
	return *n
}

func splitKey(key string) (kind, name string) {
	kind, name, _ = strings.Cut(key, "/")
	return kind, name
}

// backupReady reports whether the app's backup credentials and, with a
// database, its object store are in place and match the backup target.
func backupReady(ctx context.Context, c client.Reader, app *cellv1.App, t *backupTarget) (bool, error) {
	for _, want := range backupResources(app, t) {
		live := want.DeepCopyObject().(client.Object)
		if err := c.Get(ctx, client.ObjectKeyFromObject(want), live); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		switch w := want.(type) {
		case *corev1.Secret:
			for k, v := range w.StringData {
				if string(live.(*corev1.Secret).Data[k]) != v {
					return false, nil
				}
			}
		case *unstructured.Unstructured:
			for _, f := range []string{"destinationPath", "endpointURL"} {
				a, _, _ := unstructured.NestedString(w.Object, "spec", "configuration", f)
				b, _, _ := unstructured.NestedString(live.(*unstructured.Unstructured).Object, "spec", "configuration", f)
				if a != b {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

// chartSettings reads what the Helm releases Flux last deployed in ns set
// for replicas and CronJob suspension, with Kubernetes' defaults where the
// chart sets nothing ("Deployment/web" -> "1", "CronJob/nightly" -> "false").
func chartSettings(ctx context.Context, c client.Reader, ns string) (map[string]string, error) {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(gvkHelmRelease.GroupVersion().WithKind("HelmReleaseList"))
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	settings := map[string]string{}
	for _, hr := range list.Items {
		history, _, _ := unstructured.NestedSlice(hr.Object, "status", "history")
		if len(history) == 0 {
			continue
		}
		latest, _ := history[0].(map[string]any)
		storage, _, _ := unstructured.NestedString(hr.Object, "status", "storageNamespace")
		if storage == "" {
			storage = ns
		}
		var secret corev1.Secret
		key := client.ObjectKey{Namespace: storage, Name: fmt.Sprintf("sh.helm.release.v1.%v.v%v", latest["name"], latest["version"])}
		if err := c.Get(ctx, key, &secret); err != nil {
			return nil, fmt.Errorf("Helm release of %s: %w", hr.GetName(), err)
		}
		manifest, err := releaseManifest(secret.Data["release"])
		if err != nil {
			return nil, fmt.Errorf("Helm release %s: %w", key.Name, err)
		}
		dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
		for {
			var obj map[string]any
			if err := dec.Decode(&obj); err == io.EOF {
				break
			} else if err != nil {
				return nil, fmt.Errorf("Helm release %s: %w", key.Name, err)
			}
			u := unstructured.Unstructured{Object: obj}
			if obj == nil || (u.GetNamespace() != "" && u.GetNamespace() != ns) {
				continue
			}
			switch u.GetKind() {
			case "Deployment", "StatefulSet":
				n := int64(1)
				switch v, _, _ := unstructured.NestedFieldNoCopy(obj, "spec", "replicas"); v := v.(type) {
				case float64: // numbers decoded from JSON
					n = int64(v)
				case int64:
					n = v
				}
				settings[u.GetKind()+"/"+u.GetName()] = fmt.Sprint(n)
			case "CronJob":
				suspend, _, _ := unstructured.NestedBool(obj, "spec", "suspend")
				settings["CronJob/"+u.GetName()] = fmt.Sprint(suspend)
			}
		}
	}
	return settings, nil
}

// releaseManifest decodes a Helm release record (base64 of gzipped JSON).
func releaseManifest(data []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return "", err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	var release struct {
		Manifest string `json:"manifest"`
	}
	if err := json.NewDecoder(zr).Decode(&release); err != nil {
		return "", err
	}
	return release.Manifest, nil
}
