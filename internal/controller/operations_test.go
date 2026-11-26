package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// The tests below play CloudNativePG (database backups, recovery) and the
// Job controller (restic) by writing their status.

// completeJob finishes a Job the way the Job controller would, with the
// container's termination message.
func completeJob(t *testing.T, ns, prefix, message string) *batchv1.Job {
	t.Helper()
	ctx := context.Background()
	var job *batchv1.Job
	eventually(t, func() error {
		var jobs batchv1.JobList
		if err := k8s.List(ctx, &jobs, client.InNamespace(ns)); err != nil {
			return err
		}
		for i := range jobs.Items {
			if strings.HasPrefix(jobs.Items[i].Name, prefix) && len(jobs.Items[i].Status.Conditions) == 0 {
				job = &jobs.Items[i]
				return nil
			}
		}
		return fmt.Errorf("no running job %s* in %s", prefix, ns)
	})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-" + string(job.UID)[:8], Namespace: ns,
			Labels: map[string]string{"job-name": job.Name, "batch.kubernetes.io/controller-uid": string(job.UID)}},
		Spec: job.Spec.Template.Spec,
	}
	if err := k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "restic",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: message}}}}
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	job.Status.StartTime, job.Status.CompletionTime, job.Status.Succeeded = &now, &now, 1
	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
	}
	if err := k8s.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

// completeDatabaseBackup plays CloudNativePG finishing a base backup.
func completeDatabaseBackup(t *testing.T, ns, name, id string) {
	t.Helper()
	var pg *unstructured.Unstructured
	eventually(t, func() (err error) { pg, err = get(context.Background(), gvkCNPGBackup, ns, name); return err })
	_ = unstructured.SetNestedField(pg.Object, "completed", "status", "phase")
	_ = unstructured.SetNestedField(pg.Object, id, "status", "backupId")
	_ = unstructured.SetNestedField(pg.Object, pg.GetAnnotations()[annotationCluster], "status", "pluginMetadata", "clusterUID")
	end := time.Now().UTC().Format(time.RFC3339)
	_ = unstructured.SetNestedField(pg.Object, end, "status", "stoppedAt")
	// The plugin records the backup in the store of the archive it went to.
	archived(t, ns, pg.GetAnnotations()[annotationServer], end)
	if err := k8s.Status().Update(context.Background(), pg); err != nil {
		t.Fatal(err)
	}
}

// archived plays the barman-cloud plugin recording a base backup in
// archive sn.
func archived(t *testing.T, ns, sn, at string) {
	t.Helper()
	eventually(t, func() error {
		store, err := get(context.Background(), gvkObjectStore, ns, archiveStore(sn))
		if err != nil {
			return err
		}
		_ = unstructured.SetNestedField(store.Object, at, "status", "serverRecoveryWindow", sn, "lastSuccessfulBackupTime")
		return k8s.Status().Update(context.Background(), store)
	})
}

// completeBackup plays everything a Backup waits for and returns it. The
// snapshot is tagged like the real script does: with the job's volumes.
func completeBackup(t *testing.T, ns, name, dbID, snapshotID string) *cellv1.Backup {
	t.Helper()
	completeDatabaseBackup(t, ns, name, dbID)
	var job batchv1.Job
	eventually(t, func() error {
		return k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: jobName("backup", name)}, &job)
	})
	tags := []string{"backup=" + name, "claims=recorded"}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CLAIMS" {
			for _, c := range strings.Fields(e.Value) {
				tags = append(tags, "claim="+c)
			}
		}
	}
	raw, _ := json.Marshal([]map[string]any{{"id": snapshotID, "tags": tags}})
	completeJob(t, ns, jobName("backup", name), string(raw))
	var b cellv1.Backup
	eventually(t, func() error {
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, &b); err != nil {
			return err
		}
		if b.Status.Phase != cellv1.RunCompleted {
			return fmt.Errorf("backup %s: %s %s", name, b.Status.Phase, b.Status.Message)
		}
		return nil
	})
	return &b
}

func changePhase(t *testing.T, name string, want cellv1.RunPhase) *cellv1.ChangeRequest {
	t.Helper()
	var cr cellv1.ChangeRequest
	eventually(t, func() error {
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, &cr); err != nil {
			return err
		}
		if cr.Status.Phase != want {
			return fmt.Errorf("change %s is %s (%s), want %s", name, cr.Status.Phase, cr.Status.Message, want)
		}
		return nil
	})
	return &cr
}

// readyLedger installs an app with database and volume and brings it to
// Ready, playing the controllers it waits for.
func readyLedger(t *testing.T) *cellv1.App { return readyDataApp(t, "ledger") }

func readyDataApp(t *testing.T, name string) *cellv1.App {
	t.Helper()
	ctx := context.Background()
	ns := "app-" + name
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cellv1.AppSpec{
			Version: "1.0",
			Source:  cellv1.Source{Image: &cellv1.ImageSource{Repository: "example/ledger"}},
			Needs: cellv1.Needs{
				Database: &cellv1.Database{Engine: "postgres"},
				Volumes:  []cellv1.Volume{{Name: "files", Size: resource.MustParse("1Gi"), MountPath: "/files"}},
			},
			Access: cellv1.Access{Subdomain: name, Port: 80, Auth: cellv1.Auth{Mode: "none"}},
			Health: cellv1.Health{TimeoutSeconds: 60},
			// A scheduled backup would hold the app until a test completes it.
			Backup: cellv1.BackupPolicy{Schedule: "none"},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	var cluster *unstructured.Unstructured
	eventually(t, func() (err error) { cluster, err = get(ctx, gvkCluster, ns, "db"); return err })
	if sn := serverName(cluster); !strings.HasPrefix(sn, cryptArchive) {
		t.Fatalf("database without encrypted WAL archive: %v", cluster.Object["spec"])
	}
	for _, store := range []string{objectStore, cryptStore} {
		eventually(t, func() error { _, err := get(ctx, gvkObjectStore, ns, store); return err })
	}
	setReady(t, cluster)
	kubelet(t, ns, name)
	healthy.Store(name, true)
	issued(t, name, name+".cell.localhost")
	return phase(t, name, cellv1.PhaseReady)
}

// A ready app is backed up at once (schedule), database first, then
// its volumes, and the app records the backup.
func TestScheduledBackup(t *testing.T) {
	ctx := context.Background()
	app := readyLedger(t)
	defer removeApp(t, app)
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(app), app); err != nil {
			return err
		}
		app.Spec.Backup.Schedule = "daily"
		return k8s.Update(ctx, app)
	})

	var b cellv1.Backup
	eventually(t, func() error {
		var list cellv1.BackupList
		if err := k8s.List(ctx, &list); err != nil {
			return err
		}
		for _, x := range list.Items {
			if x.Spec.App == "ledger" && x.Spec.Reason == "scheduled" {
				b = x
				return nil
			}
		}
		return fmt.Errorf("no scheduled backup")
	})
	done := completeBackup(t, "app-ledger", b.Name, "20261119T030000", "snap-1")
	if done.Status.Database == nil || done.Status.Database.BackupID != "20261119T030000" || done.Status.VolumesSnapshot != "snap-1" {
		t.Errorf("backup status %+v", done.Status)
	}

	var job batchv1.Job
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "app-ledger", Name: jobName("backup", b.Name)}, &job); err != nil {
		t.Fatal(err)
	}
	c := job.Spec.Template.Spec.Containers[0]
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["RESTIC_REPOSITORY"] != "s3:http://backup-gateway.cell-system.svc:8080/volumes" ||
		env["DB_BACKUP"] != "20261119T030000" || !strings.HasPrefix(env["DB_SERVER"], cryptArchive) {
		t.Errorf("backup job env %v", env)
	}
	mounted := false
	for _, m := range c.VolumeMounts {
		mounted = mounted || (m.MountPath == "/data/files" && m.ReadOnly)
	}
	if !mounted {
		t.Errorf("volume not mounted read-only: %+v", c.VolumeMounts)
	}
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "ledger"}, &a)
		if a.Status.LastBackup == nil || a.Status.LastBackup.Name != b.Name {
			return fmt.Errorf("last backup %+v", a.Status.LastBackup)
		}
		return nil
	})
}

// A change waits for approval, takes a backup, and succeeds once the
// app is healthy with it. A change that keeps the app unhealthy is rolled
// back: previous spec, data restored from the backup, change log entry.
func TestChangeRequests(t *testing.T) {
	ctx := context.Background()
	app := readyLedger(t)
	defer removeApp(t, app)
	change := func(name, version string) *cellv1.ChangeRequest {
		cr := &cellv1.ChangeRequest{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: cellv1.ChangeRequestSpec{
				App: "ledger", Requester: cellv1.Requester{Kind: "Human", Name: "alice"}, Reason: "update",
				Patch:    &apiextensionsv1.JSON{Raw: []byte(fmt.Sprintf(`{"version":%q}`, version))},
				Approval: cellv1.Approval{Required: true},
			},
		}
		if err := k8s.Create(ctx, cr); err != nil {
			t.Fatal(err)
		}
		changePhase(t, name, cellv1.RunWaitingForApproval)
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "ledger"}, &a)
		if a.Spec.Version != "1.0" && name == "update-ok" {
			t.Fatal("change applied without approval")
		}
		eventually(t, func() error {
			_ = k8s.Get(ctx, client.ObjectKey{Name: name}, cr)
			cr.Spec.Approval.Decision, cr.Spec.Approval.By = "Approved", "bob"
			return k8s.Update(ctx, cr)
		})
		return cr
	}

	// Success: approved, backed up, applied, healthy.
	change("update-ok", "1.1")
	completeBackup(t, "app-ledger", "update-ok-pre", "pre-1", "snap-1")
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "ledger"}, &a)
		if a.Spec.Version != "1.1" || a.Status.Phase != cellv1.PhaseUpdating {
			return fmt.Errorf("app %s %s", a.Spec.Version, a.Status.Phase)
		}
		return nil
	})
	time.Sleep(500 * time.Millisecond)
	kubelet(t, "app-ledger", "ledger")
	ok := changePhase(t, "update-ok", cellv1.RunSucceeded)
	if ok.Status.PreviousSpec == nil || !strings.Contains(string(ok.Status.PreviousSpec.Raw), `"version":"1.0"`) {
		t.Errorf("previous spec not recorded: %v", ok.Status.PreviousSpec)
	}
	phase(t, "ledger", cellv1.PhaseReady)

	// Failure: the new version never becomes healthy.
	change("update-bad", "9.9")
	completeBackup(t, "app-ledger", "update-bad-pre", "pre-2", "snap-2")
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "ledger"}, &a)
		if a.Spec.Version != "9.9" {
			return fmt.Errorf("change not applied yet")
		}
		return nil
	})
	skew.Store(int64(2 * time.Minute)) // past the app's health timeout
	defer skew.Store(0)
	eventually(t, func() error {
		var rs cellv1.Restore
		return k8s.Get(ctx, client.ObjectKey{Name: "update-bad-rollback"}, &rs)
	})
	var a cellv1.App
	_ = k8s.Get(ctx, client.ObjectKey{Name: "ledger"}, &a)
	if a.Spec.Version != "1.1" {
		t.Errorf("spec not rolled back: %s", a.Spec.Version)
	}

	// The restore stops the app, restores volumes and database.
	eventually(t, func() error {
		var d appsv1.Deployment
		_ = k8s.Get(ctx, client.ObjectKey{Namespace: "app-ledger", Name: "ledger"}, &d)
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
			return fmt.Errorf("app not stopped for restore")
		}
		return nil
	})
	if p := phase(t, "ledger", cellv1.PhaseRollingBack); apimeta.IsStatusConditionTrue(p.Status.Conditions, "Ready") {
		t.Error("app reported ready while its data is being replaced")
	}
	restoreJob := completeJob(t, "app-ledger", jobName("restore", "update-bad-rollback"), "[]")
	env := map[string]string{}
	for _, e := range restoreJob.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["SNAPSHOT"] != "snap-2" || env["CLAIMS"] != "files" {
		t.Errorf("restore job env %v", env)
	}
	var recovered *unstructured.Unstructured
	eventually(t, func() error {
		c, err := get(ctx, gvkCluster, "app-ledger", "db")
		if err != nil {
			return err
		}
		var rs cellv1.Restore
		_ = k8s.Get(ctx, client.ObjectKey{Name: "update-bad-rollback"}, &rs)
		if c.GetAnnotations()[restoreAnnotation] != string(rs.UID) {
			return fmt.Errorf("database not recreated yet")
		}
		recovered = c
		return nil
	})
	target, _, _ := unstructured.NestedMap(recovered.Object, "spec", "bootstrap", "recovery", "recoveryTarget")
	if target["backupID"] != "pre-2" {
		t.Errorf("recovery target %v", target)
	}
	setReady(t, recovered)
	eventually(t, func() error {
		var d appsv1.Deployment
		_ = k8s.Get(ctx, client.ObjectKey{Namespace: "app-ledger", Name: "ledger"}, &d)
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 || !strings.HasSuffix(d.Spec.Template.Spec.Containers[0].Image, ":1.1") {
			return fmt.Errorf("app not started on the previous version")
		}
		return nil
	})
	time.Sleep(500 * time.Millisecond)
	kubelet(t, "app-ledger", "ledger")
	bad := changePhase(t, "update-bad", cellv1.RunRolledBack)
	if !strings.Contains(bad.Status.Message, "back on 1.1") {
		t.Errorf("change log message %q", bad.Status.Message)
	}
	attention := phase(t, "ledger", cellv1.PhaseNeedsAttention)
	if attention.Status.Version != "1.1" {
		t.Errorf("version after rollback %s", attention.Status.Version)
	}
	diagnosed(t, "ledger", "update-bad")
}

// diagnosed waits for the operator's diagnose task for app whose input
// names cause.
func diagnosed(t *testing.T, app, cause string) {
	t.Helper()
	eventually(t, func() error {
		var tasks cellv1.TaskList
		if err := k8s.List(context.Background(), &tasks); err != nil {
			return err
		}
		for _, task := range tasks.Items {
			if task.Spec.App == app && task.Spec.Kind == "diagnose" && strings.Contains(task.Spec.Input, cause) {
				return nil
			}
		}
		return fmt.Errorf("no diagnose task for %s naming %s: %+v", app, cause, tasks.Items)
	})
}

// A rejected change never touches the app.
func TestRejectedChange(t *testing.T) {
	ctx := context.Background()
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "nope"},
		Spec: cellv1.ChangeRequestSpec{
			App: "missing-app", Requester: cellv1.Requester{Kind: "Agent", Name: "ops"},
			Patch:    &apiextensionsv1.JSON{Raw: []byte(`{"version":"2"}`)},
			Approval: cellv1.Approval{Required: true, Decision: "Rejected", By: "bob"},
		},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	got := changePhase(t, "nope", cellv1.RunFailed) // app does not exist: fails first
	if got.Status.FinishedAt == nil {
		t.Error("no finish time")
	}
	raw, _ := json.Marshal(got.Spec.Patch)
	if string(raw) != `{"version":"2"}` {
		t.Errorf("patch changed: %s", raw)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Name: "missing-app"}, &cellv1.App{}); !apierrors.IsNotFound(err) {
		t.Error("rejected change created something")
	}
}

// failJob fails a Job the way the Job controller would.
func failJob(t *testing.T, ns, prefix, message string) {
	t.Helper()
	ctx := context.Background()
	var job *batchv1.Job
	eventually(t, func() error {
		var jobs batchv1.JobList
		if err := k8s.List(ctx, &jobs, client.InNamespace(ns)); err != nil {
			return err
		}
		for i := range jobs.Items {
			if strings.HasPrefix(jobs.Items[i].Name, prefix) && len(jobs.Items[i].Status.Conditions) == 0 {
				job = &jobs.Items[i]
				return nil
			}
		}
		return fmt.Errorf("no running job %s* in %s", prefix, ns)
	})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-" + string(job.UID)[:8], Namespace: ns,
		Labels: map[string]string{"job-name": job.Name, "batch.kubernetes.io/controller-uid": string(job.UID)}}, Spec: job.Spec.Template.Spec}
	if err := k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "restic",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: message}}}}
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	job.Status.StartTime, job.Status.Failed = &now, 1
	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: now},
	}
	if err := k8s.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
}

func restorePhase(t *testing.T, name string, want cellv1.RunPhase) *cellv1.Restore {
	t.Helper()
	var rs cellv1.Restore
	eventually(t, func() error {
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, &rs); err != nil {
			return err
		}
		if rs.Status.Phase != want {
			return fmt.Errorf("restore %s is %s (%s), want %s", name, rs.Status.Phase, rs.Status.Message, want)
		}
		return nil
	})
	return &rs
}

func replicasOf(t *testing.T, ns, name string) int32 {
	t.Helper()
	var d appsv1.Deployment
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	return *d.Spec.Replicas
}

// Two restores of one app never run at the same time, and a restore that
// fails while replacing data keeps the app stopped until it is resolved.
func TestRestoresTakeTurnsAndFailSafe(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "vault")
	defer removeApp(t, app)
	for _, name := range []string{"vault-r1", "vault-r2"} {
		if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: cellv1.RestoreSpec{App: "vault", Backup: "latest"}}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // distinct creation seconds
	}
	restorePhase(t, "vault-r1", cellv1.RunRunning)
	if r2 := restorePhase(t, "vault-r2", cellv1.RunPending); !strings.Contains(r2.Status.Message, "vault-r1") {
		t.Errorf("second restore waits for %q", r2.Status.Message)
	}
	completeJob(t, "app-vault", jobName("lookup", "vault-r1"),
		`[{"id":"snap-9","tags":["backup=b9","db-server=db-x","db-backup=id-9"]}]`)
	eventually(t, func() error {
		if n := replicasOf(t, "app-vault", "vault"); n != 0 {
			return fmt.Errorf("replicas %d", n)
		}
		return nil
	})
	failJob(t, "app-vault", jobName("restore", "vault-r1"), "restic: repository unreachable")
	if r1 := restorePhase(t, "vault-r1", cellv1.RunFailed); r1.Status.Step != stepVolumes {
		t.Errorf("failed in step %s", r1.Status.Step)
	}
	// The second restore may go now; take it out of the way.
	restorePhase(t, "vault-r2", cellv1.RunRunning)
	failJob(t, "app-vault", jobName("lookup", "vault-r2"), "no such backup")
	restorePhase(t, "vault-r2", cellv1.RunFailed)

	attention := phase(t, "vault", cellv1.PhaseNeedsAttention)
	if !strings.Contains(apimeta.FindStatusCondition(attention.Status.Conditions, "Ready").Message, "vault-r1") {
		t.Errorf("reason %+v", attention.Status.Conditions)
	}
	diagnosed(t, "vault", "vault-r1")
	time.Sleep(time.Second)
	if n := replicasOf(t, "app-vault", "vault"); n != 0 {
		t.Fatalf("app restarted on half-restored data: %d replicas", n)
	}
	// Resolving: delete the failed restore, the app starts again.
	if err := k8s.Delete(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "vault-r1"}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if n := replicasOf(t, "app-vault", "vault"); n != 1 {
			return fmt.Errorf("replicas %d after resolving", n)
		}
		return nil
	})
}

// A change survives an operator restart between updating the app and
// recording that: it resumes instead of reporting "nothing to change".
func TestChangeResumesAfterInterruption(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "journal")
	defer removeApp(t, app)
	prev, _ := json.Marshal(app.Spec)
	base := app.Generation
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "journal-resume"},
		Spec: cellv1.ChangeRequestSpec{App: "journal", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch: &apiextensionsv1.JSON{Raw: []byte(`{"version":"1.5"}`)}, Approval: cellv1.Approval{Required: true}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	changePhase(t, "journal-resume", cellv1.RunWaitingForApproval)
	// The interrupted run: the app already carries the change ...
	app.Spec.Version = "1.5"
	if err := k8s.Update(ctx, app); err != nil {
		t.Fatal(err)
	}
	// ... and the change recorded only its backup and the original spec.
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: "journal-resume"}, cr); err != nil {
			return err
		}
		cr.Status = cellv1.ChangeRequestStatus{Phase: cellv1.RunRunning, StartedAt: ptr(metav1.Now()), AppUID: string(app.UID),
			Backup: "none", PreviousSpec: &apiextensionsv1.JSON{Raw: prev}, BaseGeneration: base}
		return k8s.Status().Update(ctx, cr)
	})
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "journal-resume"}, cr)
		if cr.Status.Phase != cellv1.RunRunning || cr.Status.AppliedGeneration <= base {
			return fmt.Errorf("change %s applied %d (%s)", cr.Status.Phase, cr.Status.AppliedGeneration, cr.Status.Message)
		}
		return nil
	})
	time.Sleep(500 * time.Millisecond)
	kubelet(t, "app-journal", "journal")
	if done := changePhase(t, "journal-resume", cellv1.RunSucceeded); done.Status.Message == "nothing to change" {
		t.Error("resumed change skipped its health check")
	}
}

// Validation: approval is required when omitted, operation targets and the
// database size cannot change.
func TestOperationValidation(t *testing.T) {
	ctx := context.Background()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cell.internal/v1alpha1", "kind": "ChangeRequest",
		"metadata": map[string]any{"name": "no-approval"},
		"spec": map[string]any{"app": "missing", "requester": map[string]any{"kind": "Agent", "name": "x"},
			"patch": map[string]any{"version": "2"}},
	}}
	if err := k8s.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	var cr cellv1.ChangeRequest
	if err := k8s.Get(ctx, client.ObjectKey{Name: "no-approval"}, &cr); err != nil || !cr.Spec.Approval.Required {
		t.Errorf("omitted approval is not required: %+v %v", cr.Spec.Approval, err)
	}
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "no-approval"}, &cr)
		cr.Spec.App = "other"
		err := k8s.Update(ctx, &cr)
		switch {
		case apierrors.IsConflict(err):
			return err // the controller wrote meanwhile: try again
		case !apierrors.IsInvalid(err):
			t.Errorf("change target changed: %v", err)
		}
		return nil
	})

	app := readyDataApp(t, "sizes")
	defer removeApp(t, app)
	app.Spec.Needs.Database.Size = resource.MustParse("50Gi")
	if err := k8s.Update(ctx, app); !apierrors.IsInvalid(err) {
		t.Errorf("database resize accepted: %v", err)
	}
}

// Apps with volumes but no database get backup credentials too.
func TestVolumeOnlyAppBackupCredentials(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "drop"},
		Spec: cellv1.AppSpec{
			Version: "1", Source: cellv1.Source{Image: &cellv1.ImageSource{Repository: "x"}},
			Needs:  cellv1.Needs{Volumes: []cellv1.Volume{{Name: "files", Size: resource.MustParse("1Gi")}}},
			Access: cellv1.Access{Subdomain: "drop", Port: 80, Auth: cellv1.Auth{Mode: "none"}},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer removeApp(t, app)
	eventually(t, func() error {
		return k8s.Get(ctx, client.ObjectKey{Namespace: "app-drop", Name: backupSecret}, &corev1.Secret{})
	})
	if _, err := get(ctx, gvkObjectStore, "app-drop", objectStore); !apierrors.IsNotFound(err) {
		t.Errorf("object store for an app without database: %v", err)
	}
}

// A queued manual restore must not block the rollback of the running
// change (which holds the app), and runs once the change is done. The
// change's name is longer than a label value may be.
func TestRollbackNotBlockedByQueuedRestore(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "atlas")
	defer removeApp(t, app)
	name := "atlas-" + strings.Repeat("x", 64) // 70 characters
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cellv1.ChangeRequestSpec{App: "atlas", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch:    &apiextensionsv1.JSON{Raw: []byte(`{"version":"9.9"}`)},
			Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "alice"}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	changePhase(t, name, cellv1.RunRunning)
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "atlas-manual"}, Spec: cellv1.RestoreSpec{App: "atlas", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	if rs := restorePhase(t, "atlas-manual", cellv1.RunPending); !strings.Contains(rs.Status.Message, name) {
		t.Errorf("manual restore waits for %q", rs.Status.Message)
	}
	completeBackup(t, "app-atlas", name+"-pre", "pre-a", "snap-a")
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "atlas"}, &a)
		if a.Spec.Version != "9.9" {
			return fmt.Errorf("change not applied")
		}
		return nil
	})
	skew.Store(int64(2 * time.Minute))
	defer skew.Store(0)
	rollback := name + "-rollback"
	restorePhase(t, rollback, cellv1.RunRunning)
	if rs := restorePhase(t, "atlas-manual", cellv1.RunPending); rs.Status.Phase != cellv1.RunPending {
		t.Fatal("manual restore started during the change")
	}
	completeJob(t, "app-atlas", jobName("restore", rollback), "[]")
	var recovered *unstructured.Unstructured
	eventually(t, func() error {
		c, err := get(ctx, gvkCluster, "app-atlas", "db")
		var rs cellv1.Restore
		_ = k8s.Get(ctx, client.ObjectKey{Name: rollback}, &rs)
		if err != nil || c.GetAnnotations()[restoreAnnotation] != string(rs.UID) {
			return fmt.Errorf("database not recovered yet: %v", err)
		}
		recovered = c
		return nil
	})
	setReady(t, recovered)
	restorePhase(t, rollback, cellv1.RunCompleted)
	time.Sleep(500 * time.Millisecond)
	kubelet(t, "app-atlas", "atlas")
	changePhase(t, name, cellv1.RunRolledBack)
	// Now the manual restore gets the app. It was asked for before the
	// rollback but fails after it, while replacing data: the app stays
	// stopped although the (later created) rollback succeeded.
	restorePhase(t, "atlas-manual", cellv1.RunRunning)
	completeJob(t, "app-atlas", jobName("lookup", "atlas-manual"),
		`[{"id":"snap-m","tags":["backup=m","db-server=db-m","db-backup=id-m","claims=recorded","claim=files"]}]`)
	failJob(t, "app-atlas", jobName("restore", "atlas-manual"), "restic: repository unreachable")
	restorePhase(t, "atlas-manual", cellv1.RunFailed)
	phase(t, "atlas", cellv1.PhaseNeedsAttention)
	time.Sleep(time.Second)
	if n := replicasOf(t, "app-atlas", "atlas"); n != 0 {
		t.Errorf("app restarted on half-restored data: %d replicas", n)
	}
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "atlas"}, &a)
		if h := a.Annotations[lockKey]; h != "" {
			return fmt.Errorf("app still held by %s", h)
		}
		return nil
	})
}

// A rollback interrupted after it was decided resumes: the app gets its
// previous spec and data back, and the change never counts as success.
func TestRollbackResumesAfterInterruption(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "quill")
	defer removeApp(t, app)
	prev, _ := json.Marshal(app.Spec)
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "quill-interrupted"},
		Spec: cellv1.ChangeRequestSpec{App: "quill", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch: &apiextensionsv1.JSON{Raw: []byte(`{"version":"3.0"}`)}, Approval: cellv1.Approval{Required: true}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	changePhase(t, "quill-interrupted", cellv1.RunWaitingForApproval)
	completedPre := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "quill-pre-x"}, Spec: cellv1.BackupSpec{App: "quill"}}
	if err := k8s.Create(ctx, completedPre); err != nil {
		t.Fatal(err)
	}
	completeBackup(t, "app-quill", "quill-pre-x", "pre-q", "snap-q")
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: "quill"}, app); err != nil {
			return err
		}
		app.Spec.Version = "3.0"
		return k8s.Update(ctx, app)
	})
	// Interrupted right after deciding to roll back.
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: "quill-interrupted"}, cr); err != nil {
			return err
		}
		cr.Status = cellv1.ChangeRequestStatus{Phase: cellv1.RunRunning, StartedAt: ptr(metav1.Now()), AppUID: string(app.UID),
			Backup: "quill-pre-x", PreviousSpec: &apiextensionsv1.JSON{Raw: prev}, BaseGeneration: app.Generation - 1,
			AppliedGeneration: app.Generation, AppliedAt: ptr(metav1.Now()), Restore: "quill-interrupted-rollback",
			Message: "unhealthy, rolling back"}
		return k8s.Status().Update(ctx, cr)
	})
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "quill"}, &a)
		if a.Spec.Version != "1.0" {
			return fmt.Errorf("spec not reverted: %s", a.Spec.Version)
		}
		return nil
	})
	rs := restorePhase(t, "quill-interrupted-rollback", cellv1.RunRunning)
	if rs.Spec.Change != "quill-interrupted" || rs.Spec.Backup != "quill-pre-x" {
		t.Errorf("rollback restore %+v", rs.Spec)
	}
	time.Sleep(time.Second)
	if c := changePhase(t, "quill-interrupted", cellv1.RunRunning); c.Status.Phase == cellv1.RunSucceeded {
		t.Fatal("interrupted rollback reported success")
	}
}

// A restore stops nothing before the App reconciler handed the app over;
// a reconcile from before the restore cannot re-apply the workload then.
func TestRestoreWaitsForHandover(t *testing.T) {
	ctx := context.Background()
	scheme := k8s.Scheme()
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "ink"}}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app-ink", Name: "ink"}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(1))}}
	c := fakeClient(scheme, app, dep)
	r := &RestoreReconciler{Client: c}
	rs := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "ink-r"}, Spec: cellv1.RestoreSpec{App: "ink"}}
	if err := c.Create(ctx, rs); err != nil {
		t.Fatal(err)
	}
	if stopped, err := r.stop(ctx, rs, "app-ink"); err != nil || stopped {
		t.Fatalf("stopped before hand-over: %v %v", stopped, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(dep), dep); err != nil || *dep.Spec.Replicas != 1 {
		t.Fatalf("scaled before hand-over: %v", err)
	}
	app.Status.PausedBy = "ink-r"
	if err := c.Status().Update(ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, err := r.stop(ctx, rs, "app-ink"); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(dep), dep); err != nil || *dep.Spec.Replicas != 0 {
		t.Errorf("not scaled after hand-over: %v %d", err, *dep.Spec.Replicas)
	}
}

// A restore claiming to roll back a change must be that change's restore.
func TestRestoreOwnershipIsChecked(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "seal")
	defer removeApp(t, app)
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "seal-orphan"},
		Spec: cellv1.RestoreSpec{App: "seal", Change: "no-such-change"}}); err != nil {
		t.Fatal(err)
	}
	if rs := restorePhase(t, "seal-orphan", cellv1.RunFailed); !strings.Contains(rs.Status.Message, "not a rollback") {
		t.Errorf("message %q", rs.Status.Message)
	}
	var a cellv1.App
	_ = k8s.Get(ctx, client.ObjectKey{Name: "seal"}, &a)
	if h := a.Annotations[lockKey]; h != "" {
		t.Errorf("orphan took the app: %s", h)
	}
}

// Rolling back a change that added a volume restores the volumes of the
// backup and leaves the new one alone.
func TestRollbackAfterAddingVolume(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "folio")
	defer removeApp(t, app)
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "folio-add-volume"},
		Spec: cellv1.ChangeRequestSpec{App: "folio", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch: &apiextensionsv1.JSON{Raw: []byte(`{"version":"2","needs":{"database":{"engine":"postgres"},"volumes":[` +
				`{"name":"files","size":"1Gi","mountPath":"/files"},{"name":"extra","size":"1Gi","mountPath":"/extra"}]}}`)},
			Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "alice"}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	pre := completeBackup(t, "app-folio", "folio-add-volume-pre", "pre-f", "snap-f")
	if !slices.Equal(pre.Status.Claims, []string{"files"}) {
		t.Errorf("backup claims %v", pre.Status.Claims)
	}
	eventually(t, func() error {
		return k8s.Get(ctx, client.ObjectKey{Namespace: "app-folio", Name: "extra"}, &corev1.PersistentVolumeClaim{})
	})
	skew.Store(int64(2 * time.Minute))
	defer skew.Store(0)
	job := completeJob(t, "app-folio", jobName("restore", "folio-add-volume-rollback"), "[]")
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CLAIMS" && e.Value != "files" {
			t.Errorf("rollback restores %q", e.Value)
		}
	}
	restorePhase(t, "folio-add-volume-rollback", cellv1.RunRunning) // past volumes, recovering the database
}

func fakeClient(scheme *runtime.Scheme, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithReturnManagedFields().
		WithStatusSubresource(&cellv1.App{}, &cellv1.Restore{}, &cellv1.ChangeRequest{}).Build()
}

// Rolling back a change that added the first volume of a database-only
// app restores no volume at all.
func TestRollbackAfterAddingFirstVolume(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "tally"},
		Spec: cellv1.AppSpec{
			Version: "1.0", Source: cellv1.Source{Image: &cellv1.ImageSource{Repository: "x"}},
			Needs:  cellv1.Needs{Database: &cellv1.Database{Engine: "postgres"}},
			Access: cellv1.Access{Subdomain: "tally", Port: 80, Auth: cellv1.Auth{Mode: "none"}},
			Health: cellv1.Health{TimeoutSeconds: 60},
			Backup: cellv1.BackupPolicy{Schedule: "none"},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer removeApp(t, app)
	var cluster *unstructured.Unstructured
	eventually(t, func() (err error) { cluster, err = get(ctx, gvkCluster, "app-tally", "db"); return err })
	setReady(t, cluster)
	kubelet(t, "app-tally", "tally")
	healthy.Store("tally", true)
	issued(t, "tally", "tally.cell.localhost")
	phase(t, "tally", cellv1.PhaseReady)

	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "tally-first-volume"},
		Spec: cellv1.ChangeRequestSpec{App: "tally", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch:    &apiextensionsv1.JSON{Raw: []byte(`{"version":"2","needs":{"volumes":[{"name":"files","size":"1Gi","mountPath":"/f"}]}}`)},
			Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "alice"}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	pre := completeBackup(t, "app-tally", "tally-first-volume-pre", "pre-t", "snap-t")
	if !pre.Status.ClaimsRecorded || len(pre.Status.Claims) != 0 {
		t.Errorf("backup claims %v recorded=%v", pre.Status.Claims, pre.Status.ClaimsRecorded)
	}
	eventually(t, func() error {
		return k8s.Get(ctx, client.ObjectKey{Namespace: "app-tally", Name: "files"}, &corev1.PersistentVolumeClaim{})
	})
	skew.Store(int64(2 * time.Minute))
	defer skew.Store(0)
	// No volume to restore: straight to the database.
	eventually(t, func() error {
		var rs cellv1.Restore
		if err := k8s.Get(ctx, client.ObjectKey{Name: "tally-first-volume-rollback"}, &rs); err != nil {
			return err
		}
		if rs.Status.Phase == cellv1.RunFailed {
			t.Fatalf("rollback failed: %s", rs.Status.Message)
		}
		if rs.Status.Step != stepDatabase {
			return fmt.Errorf("rollback at step %q", rs.Status.Step)
		}
		return nil
	})
	var jobs batchv1.JobList
	_ = k8s.List(ctx, &jobs, client.InNamespace("app-tally"))
	for _, j := range jobs.Items {
		if strings.HasPrefix(j.Name, "restore-") {
			t.Errorf("volume restore job %s for a backup without volumes", j.Name)
		}
	}
}

// Deleting a restore that is replacing data waits until it is done; the app
// stays stopped meanwhile.
func TestDeletingRunningRestoreWaits(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "ledgerx")
	defer removeApp(t, app)
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "ledgerx-r"}, Spec: cellv1.RestoreSpec{App: "ledgerx", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	completeJob(t, "app-ledgerx", jobName("lookup", "ledgerx-r"),
		`[{"id":"snap-l","tags":["backup=l","db-server=db-l","db-backup=id-l","claims=recorded","claim=files"]}]`)
	eventually(t, func() error {
		var rs cellv1.Restore
		_ = k8s.Get(ctx, client.ObjectKey{Name: "ledgerx-r"}, &rs)
		if rs.Status.Step != stepVolumes {
			return fmt.Errorf("restore at %q", rs.Status.Step)
		}
		return nil
	})
	if err := k8s.Delete(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "ledgerx-r"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	var rs cellv1.Restore
	if err := k8s.Get(ctx, client.ObjectKey{Name: "ledgerx-r"}, &rs); err != nil {
		t.Fatalf("running restore gone while replacing data: %v", err)
	}
	if n := replicasOf(t, "app-ledgerx", "ledgerx"); n != 0 {
		t.Fatalf("app restarted during the restore: %d", n)
	}
	completeJob(t, "app-ledgerx", jobName("restore", "ledgerx-r"), "[]")
	var cluster *unstructured.Unstructured
	eventually(t, func() error {
		c, err := get(ctx, gvkCluster, "app-ledgerx", "db")
		var rs cellv1.Restore
		_ = k8s.Get(ctx, client.ObjectKey{Name: "ledgerx-r"}, &rs)
		if err != nil || c.GetAnnotations()[restoreAnnotation] != string(rs.UID) {
			return fmt.Errorf("database not recovered yet: %v", err)
		}
		cluster = c
		return nil
	})
	setReady(t, cluster)
	gone(t, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "ledgerx-r"}})
	eventually(t, func() error {
		if n := replicasOf(t, "app-ledgerx", "ledgerx"); n != 1 {
			return fmt.Errorf("replicas %d after the restore ended", n)
		}
		return nil
	})
}

// A backup created again under the name of an earlier one does not inherit
// that one's results, and it records the volumes its snapshot holds.
func TestBackupRunsAreTheirOwn(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "codex")
	defer removeApp(t, app)
	b := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "codex-again"}, Spec: cellv1.BackupSpec{App: "codex"}}
	if err := k8s.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	// A volume that appears while the backup runs is not in its snapshot.
	late := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "app-codex", Name: "late"},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	done := completeBackup(t, "app-codex", "codex-again", "first", "snap-first")
	_ = k8s.Create(ctx, late)
	if !slices.Equal(done.Status.Claims, []string{"files"}) {
		t.Errorf("claims %v", done.Status.Claims)
	}
	if err := k8s.Delete(ctx, b); err != nil {
		t.Fatal(err)
	}
	gone(t, &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "codex-again"}})
	b = &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "codex-again"}, Spec: cellv1.BackupSpec{App: "codex"}}
	if err := k8s.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if err := k8s.Get(ctx, client.ObjectKey{Name: "codex-again"}, b); err != nil {
		t.Fatal(err)
	}
	if b.Status.Phase == cellv1.RunCompleted {
		t.Fatalf("new backup completed with the old run's results: %+v", b.Status)
	}
}

// recovered plays CloudNativePG: the restore's recovered database is ready.
func recovered(t *testing.T, ns, restore string) {
	t.Helper()
	ctx := context.Background()
	eventually(t, func() error {
		var rs cellv1.Restore
		if err := k8s.Get(ctx, client.ObjectKey{Name: restore}, &rs); err != nil {
			return err
		}
		c, err := get(ctx, gvkCluster, ns, "db")
		if err != nil || c.GetAnnotations()[restoreAnnotation] != string(rs.UID) {
			return fmt.Errorf("database not recovered by %s yet: %v", restore, err)
		}
		setReady(t, c)
		return nil
	})
}

// Deleting a change while it rolls back waits until it ended; it keeps the
// app meanwhile, so nothing else can touch the data.
func TestDeletingChangeDuringRollbackWaits(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "spool")
	defer removeApp(t, app)
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "spool-bad"},
		Spec: cellv1.ChangeRequestSpec{App: "spool", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch:    &apiextensionsv1.JSON{Raw: []byte(`{"version":"9"}`)},
			Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "alice"}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	completeBackup(t, "app-spool", "spool-bad-pre", "pre-s", "snap-s")
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "spool"}, &a)
		if a.Spec.Version != "9" {
			return fmt.Errorf("not applied")
		}
		return nil
	})
	skew.Store(int64(2 * time.Minute))
	defer skew.Store(0)
	restorePhase(t, "spool-bad-rollback", cellv1.RunRunning)
	if err := k8s.Delete(ctx, &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "spool-bad"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	var a cellv1.App
	_ = k8s.Get(ctx, client.ObjectKey{Name: "spool"}, &a)
	if h := a.Annotations[lockKey]; h != "ChangeRequest/spool-bad" {
		t.Fatalf("app held by %q while the rollback runs", h)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Name: "spool-bad"}, &cellv1.ChangeRequest{}); err != nil {
		t.Fatalf("running change gone: %v", err)
	}
	completeJob(t, "app-spool", jobName("restore", "spool-bad-rollback"), "[]")
	recovered(t, "app-spool", "spool-bad-rollback")
	restorePhase(t, "spool-bad-rollback", cellv1.RunCompleted)
	time.Sleep(500 * time.Millisecond)
	kubelet(t, "app-spool", "spool")
	gone(t, &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "spool-bad"}})
	eventually(t, func() error {
		_ = k8s.Get(ctx, client.ObjectKey{Name: "spool"}, &a)
		if h := a.Annotations[lockKey]; h != "" {
			return fmt.Errorf("app still held by %s", h)
		}
		return nil
	})
}

// Runs created again under an earlier run's name do their own work: a
// restore recovers the database again, a change takes a new backup.
func TestReusedRunNames(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "relic")
	defer removeApp(t, app)
	lookup := `[{"id":"snap-r","tags":["backup=r","db-server=db-r","db-backup=id-r","claims=recorded","claim=files"]}]`
	runRestore := func() {
		if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "relic-again"}, Spec: cellv1.RestoreSpec{App: "relic", Backup: "latest"}}); err != nil {
			t.Fatal(err)
		}
		completeJob(t, "app-relic", jobName("lookup", "relic-again"), lookup)
		completeJob(t, "app-relic", jobName("restore", "relic-again"), "[]")
		recovered(t, "app-relic", "relic-again")
		restorePhase(t, "relic-again", cellv1.RunCompleted)
	}
	runRestore()
	if err := k8s.Delete(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "relic-again"}}); err != nil {
		t.Fatal(err)
	}
	gone(t, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "relic-again"}})
	runRestore() // fails if the second run took the first run's database as its own
	time.Sleep(500 * time.Millisecond)
	kubelet(t, "app-relic", "relic") // restarted by the restores
	eventually(t, func() error {
		var a cellv1.App
		_ = k8s.Get(ctx, client.ObjectKey{Name: "relic"}, &a)
		if !apimeta.IsStatusConditionTrue(a.Status.Conditions, "Ready") {
			return fmt.Errorf("not ready after the restores")
		}
		return nil
	})

	change := func(timeout int) *cellv1.ChangeRequest {
		cr := &cellv1.ChangeRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "relic-change"},
			Spec: cellv1.ChangeRequestSpec{App: "relic", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
				Patch:    &apiextensionsv1.JSON{Raw: []byte(fmt.Sprintf(`{"health":{"timeoutSeconds":%d}}`, timeout))},
				Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "alice"}},
		}
		if err := k8s.Create(ctx, cr); err != nil {
			t.Fatal(err)
		}
		return cr
	}
	change(61)
	completeBackup(t, "app-relic", "relic-change-pre", "pre-1", "snap-1")
	changePhase(t, "relic-change", cellv1.RunSucceeded)
	if err := k8s.Delete(ctx, &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "relic-change"}}); err != nil {
		t.Fatal(err)
	}
	gone(t, &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "relic-change"}})
	change(62)
	time.Sleep(2 * time.Second)
	if c := changePhase(t, "relic-change", cellv1.RunRunning); c.Status.PreviousSpec != nil {
		t.Fatal("second change used the first change's backup")
	}
	completeBackup(t, "app-relic", "relic-change-pre", "pre-2", "snap-2")
	changePhase(t, "relic-change", cellv1.RunSucceeded)
}

// staleReader answers Gets of one object with an old copy of it, like an
// informer cache that has not seen the latest writes yet.
type staleReader struct {
	client.Client
	stale client.Object
}

func (s staleReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key == client.ObjectKeyFromObject(s.stale) {
		reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(s.stale.DeepCopyObject()).Elem())
		return nil
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// A run never takes the result of an earlier run's child of the same name,
// also when its reads lag behind, and never deletes its own child for it.
func TestEnsureChildIgnoresStaleReads(t *testing.T) {
	ctx := context.Background()
	mine := &cellv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "lag-pre", Labels: map[string]string{labelRun: "new-run"}},
		Status:     cellv1.BackupStatus{Phase: cellv1.RunRunning},
	}
	c := fakeClient(k8s.Scheme(), mine)
	stale := &cellv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "lag-pre", ResourceVersion: "5", Labels: map[string]string{labelRun: "old-run"}},
		Status:     cellv1.BackupStatus{Phase: cellv1.RunCompleted, VolumesSnapshot: "old"},
	}
	child := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "lag-pre"}}
	if ok, err := ensureChild(ctx, staleReader{c, stale}, child, "new-run"); err != nil || ok {
		t.Fatalf("took a stale child: %v %v", ok, err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "lag-pre"}, &cellv1.Backup{}); err != nil {
		t.Fatalf("deleted its own child for a stale read: %v", err)
	}
	child = &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "lag-pre"}}
	if ok, err := ensureChild(ctx, c, child, "new-run"); err != nil || !ok {
		t.Fatalf("own child not found: %v %v", ok, err)
	}
	if child.Status.Phase != cellv1.RunRunning || child.Status.VolumesSnapshot != "" {
		t.Errorf("child status %+v is not the current one", child.Status)
	}
}

// Removing an app waits for a restore that is writing its volumes, so no
// reinstall can start on them meanwhile; new operations are refused.
func TestRemovalWaitsForRestore(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "plume")
	defer removeApp(t, app)
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "plume-r"}, Spec: cellv1.RestoreSpec{App: "plume", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	completeJob(t, "app-plume", jobName("lookup", "plume-r"),
		`[{"id":"snap-q","tags":["backup=q","db-server=db-q","db-backup=id-q","claims=recorded","claim=files"]}]`)
	eventually(t, func() error {
		return k8s.Get(ctx, client.ObjectKey{Namespace: "app-plume", Name: jobName("restore", "plume-r")}, &batchv1.Job{})
	})
	if err := k8s.Delete(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "plume-late"}, Spec: cellv1.RestoreSpec{App: "plume", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	restorePhase(t, "plume-late", cellv1.RunFailed)
	time.Sleep(2 * time.Second)
	var a cellv1.App
	if err := k8s.Get(ctx, client.ObjectKey{Name: "plume"}, &a); err != nil {
		t.Fatalf("app removed while its restore wrote the volumes: %v", err)
	}
	if c := apimeta.FindStatusCondition(a.Status.Conditions, "Ready"); c == nil || !strings.Contains(c.Message, "Restore/plume-r") {
		t.Errorf("removal does not say what it waits for: %+v", a.Status.Conditions)
	}
	completeJob(t, "app-plume", jobName("restore", "plume-r"), "[]")
	recovered(t, "app-plume", "plume-r")
	restorePhase(t, "plume-r", cellv1.RunCompleted)
	gone(t, app)
}

// A restore stops a Helm app only once Flux has seen the suspended
// release: a reconciliation already running could bring workloads back.
func TestRestoreWaitsForFluxToPause(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "atlas"},
		Spec: cellv1.AppSpec{
			Version: "1",
			Source:  cellv1.Source{Helm: &cellv1.HelmSource{Repo: "https://charts.example.org", Chart: "atlas", Version: "1.0.0"}},
			Access:  cellv1.Access{Subdomain: "atlas", Port: 80, Auth: cellv1.Auth{Mode: "none"}},
			Backup:  cellv1.BackupPolicy{Schedule: "none"},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer removeApp(t, app)
	eventually(t, func() error { _, err := get(ctx, gvkHelmRelease, "app-atlas", "atlas"); return err })
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "atlas-r"}, Spec: cellv1.RestoreSpec{App: "atlas", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	completeJob(t, "app-atlas", jobName("lookup", "atlas-r"), `[{"id":"snap-a","tags":["backup=a","claims=recorded"]}]`)
	eventually(t, func() error {
		rs := restorePhase(t, "atlas-r", cellv1.RunRunning)
		if rs.Status.Message != "waiting for Flux to pause the Helm release" {
			return fmt.Errorf("restore at %s: %s", rs.Status.Step, rs.Status.Message)
		}
		return nil
	})
	hr, err := get(ctx, gvkHelmRelease, "app-atlas", "atlas")
	if err != nil {
		t.Fatal(err)
	}
	if suspended, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend"); !suspended {
		t.Fatal("release not suspended")
	}
	time.Sleep(time.Second)
	if rs := restorePhase(t, "atlas-r", cellv1.RunRunning); rs.Status.Step != stepStop {
		t.Fatalf("went on to %s before Flux saw the suspension", rs.Status.Step)
	}
	setReady(t, hr) // Flux handled the suspended generation
	// The App reconciler resumes the release with the app's spec; the
	// workloads run again only after Flux applied it.
	eventually(t, func() (err error) {
		if hr, err = get(ctx, gvkHelmRelease, "app-atlas", "atlas"); err != nil {
			return err
		}
		if suspended, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend"); suspended {
			return fmt.Errorf("release still suspended")
		}
		return nil
	})
	time.Sleep(time.Second)
	if rs := restorePhase(t, "atlas-r", cellv1.RunRunning); rs.Status.Step != stepStart {
		t.Fatalf("restore at %s before Flux applied the resumed release", rs.Status.Step)
	}
	deployed(t, "app-atlas", "atlas", "1.0.0", "")
	restorePhase(t, "atlas-r", cellv1.RunCompleted)
}

// A Helm app starts again only once Flux deployed the release for its
// spec. Flux records its generation also for a failed upgrade, which may
// have left the old templates in place; the restore waits, and fails when
// Flux gives up, instead of starting those templates on the restored data.
func TestRestoreStartNeedsDeployedRelease(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "beacon"},
		Spec: cellv1.AppSpec{
			Version: "1",
			Source:  cellv1.Source{Helm: &cellv1.HelmSource{Repo: "https://charts.example.org", Chart: "beacon", Version: "1.0.0"}},
			Access:  cellv1.Access{Subdomain: "beacon", Port: 80, Auth: cellv1.Auth{Mode: "none"}},
			Backup:  cellv1.BackupPolicy{Schedule: "none"},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer removeApp(t, app)
	eventually(t, func() error { _, err := get(ctx, gvkHelmRelease, "app-beacon", "beacon"); return err })
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "beacon-r"}, Spec: cellv1.RestoreSpec{App: "beacon", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	completeJob(t, "app-beacon", jobName("lookup", "beacon-r"), `[{"id":"snap-b","tags":["backup=b","claims=recorded"]}]`)
	var hr *unstructured.Unstructured
	flux := func(conds ...map[string]any) {
		t.Helper()
		eventually(t, func() (err error) {
			if hr, err = get(ctx, gvkHelmRelease, "app-beacon", "beacon"); err != nil {
				return err
			}
			list := []any{}
			for _, c := range conds {
				c["lastTransitionTime"], c["observedGeneration"] = time.Now().UTC().Format(time.RFC3339), hr.GetGeneration()
				list = append(list, c)
			}
			_ = unstructured.SetNestedField(hr.Object, hr.GetGeneration(), "status", "observedGeneration")
			_ = unstructured.SetNestedSlice(hr.Object, list, "status", "conditions")
			return k8s.Status().Update(ctx, hr)
		})
	}
	eventually(t, func() error {
		rs := restorePhase(t, "beacon-r", cellv1.RunRunning)
		if rs.Status.Step == stepStop {
			flux(map[string]any{"type": "Ready", "status": "True", "reason": "Test", "message": ""}) // saw the suspension
		}
		if rs.Status.Step != stepStart {
			return fmt.Errorf("restore at %s", rs.Status.Step)
		}
		return nil
	})
	eventually(t, func() (err error) {
		if hr, err = get(ctx, gvkHelmRelease, "app-beacon", "beacon"); err != nil {
			return err
		}
		if suspended, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend"); suspended {
			return fmt.Errorf("release still suspended")
		}
		return nil
	})
	flux(map[string]any{"type": "Ready", "status": "False", "reason": "UpgradeFailed", "message": "pre-upgrade hook failed"})
	time.Sleep(2 * time.Second)
	if rs := restorePhase(t, "beacon-r", cellv1.RunRunning); rs.Status.Step != stepStart {
		t.Fatalf("restore at %s", rs.Status.Step)
	}
	flux(map[string]any{"type": "Ready", "status": "False", "reason": "UpgradeFailed", "message": "pre-upgrade hook failed"},
		map[string]any{"type": "Stalled", "status": "True", "reason": "RetriesExceeded", "message": "Failed to upgrade after 1 attempt(s)"})
	if rs := restorePhase(t, "beacon-r", cellv1.RunFailed); !strings.Contains(rs.Status.Message, "Flux gave up deploying the app's spec (Failed to upgrade after 1 attempt(s))") {
		t.Errorf("restore failed with %q", rs.Status.Message)
	}
}

// Setting workloads back after a restore follows the deployed chart. For a
// rollback the recorded replicas and CronJob states come from the version
// rolled back: workloads of the chart get what the chart sets (Kubernetes'
// defaults where it sets nothing), also when that equals the stop's value;
// other workloads get the recorded value unless someone else set the field.
func TestRestartFollowsTheChart(t *testing.T) {
	ctx := context.Background()
	ns := "restart-chart"
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"run": "x"}
	pod := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyOnFailure, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}
	deploy := func(name string, n int32) client.Object {
		p := pod
		p.Spec.RestartPolicy = corev1.RestartPolicyAlways
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(n), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: p}}
	}
	cron := func(name string, suspend bool) client.Object {
		return &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: batchv1.CronJobSpec{Schedule: "0 3 * * *", Suspend: ptr(suspend), JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: pod}}}}
	}
	// What ran before the restore: the version being rolled back.
	recorded := map[string]int32{
		"Deployment/web": 3, "Deployment/idle": 2, "Deployment/plain": 2, "CronJob/report": 1, "CronJob/hourly": 0, // in the chart
		"Deployment/worker": 2, "Deployment/api": 3, "CronJob/nightly": 1, // not in the chart
	}
	for _, o := range []client.Object{deploy("web", 3), deploy("idle", 2), deploy("plain", 2), cron("report", false), cron("hourly", true),
		deploy("worker", 2), deploy("api", 3), cron("nightly", false)} {
		if err := k8s.Create(ctx, o, client.FieldOwner("helm")); err != nil {
			t.Fatal(err)
		}
	}
	hr := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "rel", "namespace": ns},
		"spec":     map[string]any{"interval": "10m", "chartRef": map[string]any{"kind": "OCIRepository", "name": "rel"}}}}
	if err := k8s.Create(ctx, hr); err != nil {
		t.Fatal(err)
	}
	r := &RestoreReconciler{Client: k8s}
	if err := r.scale(ctx, ns, recorded, true, nil); err != nil {
		t.Fatal(err)
	}
	// The version rolled back to, as Flux deployed it.
	_ = unstructured.SetNestedSlice(hr.Object, []any{map[string]any{"name": "rel", "namespace": ns, "version": int64(4), "status": "deployed"}}, "status", "history")
	_ = unstructured.SetNestedField(hr.Object, ns, "status", "storageNamespace")
	if err := k8s.Status().Update(ctx, hr); err != nil {
		t.Fatal(err)
	}
	helmRecord(t, ns, "rel", 4, `apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec: {replicas: 1}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: idle}
spec: {replicas: 0}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: plain}
spec: {}
---
apiVersion: batch/v1
kind: CronJob
metadata: {name: report}
spec: {suspend: true}
---
apiVersion: batch/v1
kind: CronJob
metadata: {name: hourly}
spec: {}
`)
	// The App reconciler set api's replicas since the stop.
	if err := k8s.Patch(ctx, deploy("api", 0), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"replicas":1}}`)), client.FieldOwner("cell-operator")); err != nil {
		t.Fatal(err)
	}
	if err := r.start(ctx, &cellv1.Restore{Status: cellv1.RestoreStatus{Replicas: recorded}}, ns); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for key := range recorded {
		kind, name := splitKey(key)
		if kind == "CronJob" {
			var c batchv1.CronJob
			if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &c); err != nil {
				t.Fatal(err)
			}
			got[key] = fmt.Sprint(*c.Spec.Suspend)
			continue
		}
		var d appsv1.Deployment
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
			t.Fatal(err)
		}
		got[key] = fmt.Sprint(*d.Spec.Replicas)
	}
	want := map[string]string{
		"Deployment/web": "1", "Deployment/idle": "0", "Deployment/plain": "1", "CronJob/report": "true", "CronJob/hourly": "false",
		"Deployment/worker": "2", "Deployment/api": "1", "CronJob/nightly": "false",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after the restart:\n got %v\nwant %v", got, want)
	}
}

// removeApp deletes an app at the end of a test. Removal waits for running
// operations; the ones a test left running on purpose are ended first.
func removeApp(t *testing.T, app *cellv1.App) {
	t.Helper()
	ctx := context.Background()
	eventually(t, func() error {
		var restores cellv1.RestoreList
		var changes cellv1.ChangeRequestList
		var backups cellv1.BackupList
		if err := k8s.List(ctx, &restores); err != nil {
			return err
		}
		if err := k8s.List(ctx, &changes); err != nil {
			return err
		}
		if err := k8s.List(ctx, &backups); err != nil {
			return err
		}
		var objs []client.Object
		for i := range backups.Items {
			if b := &backups.Items[i]; b.Spec.App == app.Name && !finished(b.Status.Phase) {
				b.Status.Phase, b.Status.CompletedAt = cellv1.RunFailed, ptr(metav1.Now())
				objs = append(objs, b)
			}
		}
		for i := range restores.Items {
			if rs := &restores.Items[i]; rs.Spec.App == app.Name && !finished(rs.Status.Phase) {
				rs.Status.Phase, rs.Status.CompletedAt = cellv1.RunFailed, ptr(metav1.Now())
				objs = append(objs, rs)
			}
		}
		for i := range changes.Items {
			if cr := &changes.Items[i]; cr.Spec.App == app.Name && !finished(cr.Status.Phase) {
				cr.Status.Phase, cr.Status.FinishedAt = cellv1.RunFailed, ptr(metav1.Now())
				objs = append(objs, cr)
			}
		}
		for _, o := range objs {
			if err := k8s.Status().Update(ctx, o); err != nil {
				return err
			}
		}
		return nil
	})
	if err := k8s.Delete(ctx, app); client.IgnoreNotFound(err) != nil {
		t.Fatal(err)
	}
	gone(t, app)
}

// A restore stops what else writes to the app's volumes: CronJobs are
// suspended (and resumed only if they were active), the app's own Jobs
// finish first. The operator's Jobs and the database do not count.
func TestRestoreStopsAppJobs(t *testing.T) {
	ctx := context.Background()
	ns := "app-cron"
	running := func(name string, labels map[string]string) (*batchv1.Job, *corev1.Pod) {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name + "-x", Labels: map[string]string{"job-name": name}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	}
	appJob, appPod := running("cleanup", nil)
	opJob, opPod := running("restore-cron-r", map[string]string{labelRun: "some-run"})
	db := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "db-1", Labels: map[string]string{"cnpg.io/cluster": "db"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	nightly := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "nightly"}}
	paused := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "paused"}, Spec: batchv1.CronJobSpec{Suspend: ptr(true)}}
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "cron"}, Status: cellv1.AppStatus{PausedBy: "cron-r"}}
	c := fakeClient(k8s.Scheme(), app, appJob, appPod, opJob, opPod, db, nightly, paused)
	r := &RestoreReconciler{Client: c}
	rs := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "cron-r"}, Spec: cellv1.RestoreSpec{App: "cron"}}
	if err := c.Create(ctx, rs); err != nil {
		t.Fatal(err)
	}
	suspended := func(name string) bool {
		var cj batchv1.CronJob
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &cj); err != nil {
			t.Fatal(err)
		}
		return cj.Spec.Suspend != nil && *cj.Spec.Suspend
	}
	if stopped, err := r.stop(ctx, rs, ns); err != nil || stopped || rs.Status.Message != "waiting for job cleanup to finish" {
		t.Fatalf("stopped while the app's job runs: %v %v %q", stopped, err, rs.Status.Message)
	}
	if !suspended("nightly") {
		t.Error("cron job not suspended")
	}
	appJob.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	appPod.Status.Phase = corev1.PodSucceeded
	for _, o := range []client.Object{appJob, appPod} {
		if err := c.Status().Update(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if stopped, err := r.stop(ctx, rs, ns); err != nil || !stopped {
		t.Fatalf("not stopped after the app's job finished: %v %v %q", stopped, err, rs.Status.Message)
	}
	if err := r.start(ctx, rs, ns); err != nil {
		t.Fatal(err)
	}
	if suspended("nightly") || !suspended("paused") {
		t.Errorf("after the restore: nightly suspended %v, paused suspended %v", suspended("nightly"), suspended("paused"))
	}
}

// Volumes cannot be resized: a change that grows one fails before it is
// applied, so no rollback has to shrink it.
func TestVolumeResizeIsRejected(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "vellum")
	defer removeApp(t, app)
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "vellum-grow"},
		Spec: cellv1.ChangeRequestSpec{App: "vellum", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch:    &apiextensionsv1.JSON{Raw: []byte(`{"needs":{"volumes":[{"name":"files","size":"2Gi","mountPath":"/files"}]}}`)},
			Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "alice"}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	completeBackup(t, "app-vellum", "vellum-grow-pre", "pre-v", "snap-v")
	if done := changePhase(t, "vellum-grow", cellv1.RunFailed); !strings.Contains(done.Status.Message, "resizing a volume is not supported") {
		t.Errorf("change failed with %q", done.Status.Message)
	}
	var a cellv1.App
	if err := k8s.Get(ctx, client.ObjectKey{Name: "vellum"}, &a); err != nil || a.Spec.Needs.Volumes[0].Size.String() != "1Gi" {
		t.Errorf("volume changed: %v %+v", err, a.Spec.Needs.Volumes)
	}
}

// A restore waits until the app was set up (namespace, backup credentials,
// volumes); it does not pause an app that is still being installed.
func TestRestoreWaitsForAppSetup(t *testing.T) {
	ctx := context.Background()
	app := &cellv1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh"},
		Spec: cellv1.AppSpec{
			Version: "1.0",
			Source:  cellv1.Source{Image: &cellv1.ImageSource{Repository: "example/ledger"}},
			Needs:   cellv1.Needs{Database: &cellv1.Database{Engine: "postgres"}},
			Access:  cellv1.Access{Subdomain: "fresh", Port: 80, Auth: cellv1.Auth{Mode: "none"}},
		},
	}
	if err := k8s.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer removeApp(t, app)
	var cluster *unstructured.Unstructured
	eventually(t, func() (err error) { cluster, err = get(ctx, gvkCluster, "app-fresh", "db"); return err })
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "fresh-r"}, Spec: cellv1.RestoreSpec{App: "fresh", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if rs := restorePhase(t, "fresh-r", cellv1.RunPending); rs.Status.Message != "waiting for the app to be set up" {
		t.Errorf("restore waits for %q", rs.Status.Message)
	}
	if a := phase(t, "fresh", cellv1.PhaseInstalling); a.Status.PausedBy != "" {
		t.Errorf("paused by %s during installation", a.Status.PausedBy)
	}
	setReady(t, cluster)
	restorePhase(t, "fresh-r", cellv1.RunRunning)
}

// Change names leave room for their children's suffixes.
func TestChangeNameLimit(t *testing.T) {
	ctx := context.Background()
	change := func(name string) error {
		return k8s.Create(ctx, &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: cellv1.ChangeRequestSpec{App: "nowhere", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
				Patch: &apiextensionsv1.JSON{Raw: []byte(`{}`)}}})
	}
	if err := change(strings.Repeat("a", 245)); !apierrors.IsInvalid(err) {
		t.Errorf("245 characters: %v", err)
	}
	longest := strings.Repeat("b", 244)
	if err := change(longest); err != nil {
		t.Fatalf("244 characters: %v", err)
	}
	for _, child := range []string{longest + "-pre", longest + "-rollback"} {
		if errs := validation.IsDNS1123Subdomain(child); len(errs) > 0 {
			t.Errorf("child name %s...: %v", child[:10], errs)
		}
	}
	_ = k8s.Delete(ctx, &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: longest}})
}

// A restore does not pause an app whose backup credentials are missing or
// out of date (backups enabled after the app was set up): the paused app
// could not write them, and the restore would wait for them forever.
func TestRestoreWaitsForBackupCredentials(t *testing.T) {
	ctx := context.Background()
	targetSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "backup-target"},
		Data: map[string][]byte{"password": []byte("pw"), "gatewayKey": []byte("gk"), "prefix": []byte("p"), "rclone.conf": []byte("[target]\ntype = local\n")}}
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "keel"},
		Spec:   cellv1.AppSpec{Needs: cellv1.Needs{Volumes: []cellv1.Volume{{Name: "files"}}}},
		Status: cellv1.AppStatus{Conditions: []metav1.Condition{{Type: condWorkload, Status: metav1.ConditionTrue}}}}
	rs := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "keel-r", Finalizers: []string{restoreFinalizer}},
		Spec: cellv1.RestoreSpec{App: "keel", Backup: "latest"}}
	c := fakeClient(k8s.Scheme(), targetSecret, app, rs)
	r := &RestoreReconciler{Client: c}
	step := func() *cellv1.Restore {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rs)}); err != nil {
			t.Fatal(err)
		}
		var got cellv1.Restore
		if err := c.Get(ctx, client.ObjectKeyFromObject(rs), &got); err != nil {
			t.Fatal(err)
		}
		return &got
	}
	waiting := func(when string) {
		t.Helper()
		if got := step(); got.Status.Phase != cellv1.RunPending || got.Status.Message != "waiting for the app's backup credentials" {
			t.Fatalf("%s: %s %q", when, got.Status.Phase, got.Status.Message)
		}
		var a cellv1.App
		if err := c.Get(ctx, client.ObjectKey{Name: "keel"}, &a); err != nil || a.Annotations[lockKey] != "" {
			t.Fatalf("%s: app held by %q (%v)", when, a.Annotations[lockKey], err)
		}
	}
	waiting("no credentials")
	target, err := loadTarget(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	creds := backupResources(app, target)[0].(*corev1.Secret)
	creds.Data = map[string][]byte{}
	for k, v := range creds.StringData {
		creds.Data[k] = []byte(v)
	}
	creds.StringData, creds.Data["RESTIC_PASSWORD"] = nil, []byte("old")
	if err := c.Create(ctx, creds); err != nil {
		t.Fatal(err)
	}
	waiting("stale credentials")
	creds.Data["RESTIC_PASSWORD"] = []byte("pw")
	if err := c.Update(ctx, creds); err != nil {
		t.Fatal(err)
	}
	if got := step(); got.Status.Phase != cellv1.RunRunning {
		t.Fatalf("with current credentials: %s %q", got.Status.Phase, got.Status.Message)
	}
}

// Generated job names are valid object names and label values, also when a
// dotted run name is cut short.
func TestJobNameIsValid(t *testing.T) {
	for _, name := range []string{"r1", "a.b", strings.Repeat("a", 46) + "." + strings.Repeat("b", 20),
		strings.Repeat("c", 46) + "-" + strings.Repeat("d", 20), strings.Repeat("e.", 120) + "e"} {
		n := jobName("backup", name)
		if errs := append(validation.IsDNS1123Subdomain(n), validation.IsValidLabelValue(n)...); len(errs) > 0 {
			t.Errorf("%s: %v", n, errs)
		}
	}
	if jobName("backup", strings.Repeat("a", 60)) == jobName("backup", strings.Repeat("a", 61)) {
		t.Error("cut names collide")
	}
}

// Restarting after a restore waits until the app is configured for its
// current spec again. A rollback reverted the spec, but the App reconciler
// may not have applied it before the restore stopped the app: scaling up
// then would run the version being rolled back on the restored data.
func TestRestoreStartWaitsForAppConfiguration(t *testing.T) {
	ctx := context.Background()
	targetSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "backup-target"},
		Data: map[string][]byte{"password": []byte("pw"), "gatewayKey": []byte("gk"), "prefix": []byte("p"), "rclone.conf": []byte("[target]\ntype = local\n")}}
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "loom", UID: "loom-uid", Generation: 3},
		Status: cellv1.AppStatus{WorkloadGeneration: 2}} // reverted spec (3) not applied yet
	web := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app-loom", Name: "web"}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(1))}}
	rs := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "loom-rollback", Finalizers: []string{restoreFinalizer}},
		Spec: cellv1.RestoreSpec{App: "loom", Change: "loom-update"},
		Status: cellv1.RestoreStatus{Phase: cellv1.RunRunning, Step: stepStart, AppUID: "loom-uid",
			Replicas: map[string]int32{"Deployment/web": 1}, Resolved: &cellv1.ResolvedBackup{Backup: "loom-update-pre"}}}
	c := fakeClient(k8s.Scheme(), targetSecret, app, web, rs)
	r := &RestoreReconciler{Client: c}
	if err := r.scale(ctx, "app-loom", rs.Status.Replicas, true, nil); err != nil { // the stop step
		t.Fatal(err)
	}
	step := func() (*cellv1.Restore, int32) {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rs)}); err != nil {
			t.Fatal(err)
		}
		var got cellv1.Restore
		var d appsv1.Deployment
		if err := c.Get(ctx, client.ObjectKeyFromObject(rs), &got); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(web), &d); err != nil {
			t.Fatal(err)
		}
		return &got, *d.Spec.Replicas
	}
	if got, n := step(); got.Status.Phase != cellv1.RunRunning || n != 0 {
		t.Fatalf("before the app applied its spec: %s, %d replicas", got.Status.Phase, n)
	}
	app.Status.WorkloadGeneration = 3
	if err := c.Status().Update(ctx, app); err != nil {
		t.Fatal(err)
	}
	if got, n := step(); got.Status.Phase != cellv1.RunCompleted || n != 1 {
		t.Fatalf("after the app applied its spec: %s %q, %d replicas", got.Status.Phase, got.Status.Message, n)
	}

	// Meanwhile the App reconciler manages the app again, also when an
	// earlier restore failed halfway (that one keeps the app stopped until
	// a restore succeeds, which this one can only do with the app running).
	failed := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "loom-old", CreationTimestamp: metav1.Now()},
		Spec: cellv1.RestoreSpec{App: "loom"}, Status: cellv1.RestoreStatus{Phase: cellv1.RunFailed, Step: stepVolumes, CompletedAt: ptr(metav1.Now())}}
	starting := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "loom-new", CreationTimestamp: metav1.Now()},
		Spec: cellv1.RestoreSpec{App: "loom"}, Status: cellv1.RestoreStatus{Phase: cellv1.RunRunning, Step: stepStart}}
	ar := &AppReconciler{Client: fakeClient(k8s.Scheme(), failed, starting)}
	act, err := ar.activity(ctx, &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "loom"}})
	if err != nil {
		t.Fatal(err)
	}
	if act.paused || act.phase != cellv1.PhaseRestoring {
		t.Errorf("restore at start: paused %v, phase %s", act.paused, act.phase)
	}
}

// A restore that succeeds after a failed update is the app's latest change:
// the app does not keep the update's failure.
func TestRestoreClearsFailedChange(t *testing.T) {
	t1, t2 := metav1.NewTime(time.Now().Add(-time.Minute)), metav1.Now()
	update := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "lark-update"},
		Spec:   cellv1.ChangeRequestSpec{App: "lark"},
		Status: cellv1.ChangeRequestStatus{Phase: cellv1.RunFailed, FinishedAt: &t1, AppliedGeneration: 2}}
	restore := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "lark-restore"},
		Spec:   cellv1.ChangeRequestSpec{App: "lark", Action: "restore", Backup: "lark-keep"},
		Status: cellv1.ChangeRequestStatus{Phase: cellv1.RunSucceeded, FinishedAt: &t2, Restore: "lark-restore-restore"}}
	ar := &AppReconciler{Client: fakeClient(k8s.Scheme(), update, restore)}
	act, err := ar.activity(context.Background(), &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "lark"}})
	if err != nil {
		t.Fatal(err)
	}
	if act.failedChange != "" {
		t.Errorf("app keeps %q after a successful restore", act.failedChange)
	}
}

// A database backup is bound to the cluster it was asked of: if a restore
// replaced the cluster before CloudNativePG ran it, the backup fails instead
// of pairing the old backup with the new cluster's WAL archive.
func TestBackupIsBoundToItsDatabase(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "tome")
	defer removeApp(t, app)
	if err := k8s.Create(ctx, &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "tome-b"}, Spec: cellv1.BackupSpec{App: "tome"}}); err != nil {
		t.Fatal(err)
	}
	var pg *unstructured.Unstructured
	eventually(t, func() (err error) { pg, err = get(ctx, gvkCNPGBackup, "app-tome", "tome-b"); return err })
	cluster, err := get(ctx, gvkCluster, "app-tome", "db")
	if err != nil {
		t.Fatal(err)
	}
	if a := pg.GetAnnotations(); a[annotationCluster] != string(cluster.GetUID()) || a[annotationServer] != serverName(cluster) {
		t.Fatalf("database backup not bound to its cluster: %v", a)
	}
	_ = unstructured.SetNestedField(pg.Object, "completed", "status", "phase")
	_ = unstructured.SetNestedField(pg.Object, "20261119T040000", "status", "backupId")
	_ = unstructured.SetNestedField(pg.Object, "a-replaced-cluster", "status", "pluginMetadata", "clusterUID")
	if err := k8s.Status().Update(ctx, pg); err != nil {
		t.Fatal(err)
	}
	var b cellv1.Backup
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: "tome-b"}, &b); err != nil {
			return err
		}
		if b.Status.Phase != cellv1.RunFailed {
			return fmt.Errorf("backup %s: %s", b.Status.Phase, b.Status.Message)
		}
		return nil
	})
	if !strings.Contains(b.Status.Message, "not of the database it was asked of") || b.Status.Database != nil {
		t.Errorf("failed with %q, database %+v", b.Status.Message, b.Status.Database)
	}
}

// Backups and restores of an app take turns: a backup would copy data a
// restore is replacing, a restore would replace data a backup is copying.
func TestBackupsAndRestoresTakeTurns(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "quire")
	defer removeApp(t, app)
	create := func(o client.Object) {
		t.Helper()
		if err := k8s.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	backupPhase := func(name string, want cellv1.RunPhase) *cellv1.Backup {
		t.Helper()
		var b cellv1.Backup
		eventually(t, func() error {
			if err := k8s.Get(ctx, client.ObjectKey{Name: name}, &b); err != nil {
				return err
			}
			if b.Status.Phase != want {
				return fmt.Errorf("backup %s is %s (%s), want %s", name, b.Status.Phase, b.Status.Message, want)
			}
			return nil
		})
		return &b
	}
	create(&cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "quire-b1"}, Spec: cellv1.BackupSpec{App: "quire"}})
	backupPhase("quire-b1", cellv1.RunRunning)
	create(&cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "quire-r"}, Spec: cellv1.RestoreSpec{App: "quire", Backup: "latest"}})
	eventually(t, func() error {
		if rs := restorePhase(t, "quire-r", cellv1.RunPending); rs.Status.Message != "waiting for Backup/quire-b1" {
			return fmt.Errorf("restore waits for %q", rs.Status.Message)
		}
		return nil
	})
	completeBackup(t, "app-quire", "quire-b1", "20261119T050000", "snap-q")
	restorePhase(t, "quire-r", cellv1.RunRunning)
	create(&cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "quire-b2"}, Spec: cellv1.BackupSpec{App: "quire"}})
	if b := backupPhase("quire-b2", cellv1.RunPending); b.Status.Message != "waiting for Restore/quire-r" {
		t.Errorf("backup waits for %q", b.Status.Message)
	}
}

// Deleting a running backup cancels it: its database backup and job are
// deleted, and it holds the app until the job's pods are gone, since a
// restore must not replace volumes a backup still copies.
func TestDeletingRunningBackupCancelsIt(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "scroll")
	defer removeApp(t, app)
	b := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "scroll-b"}, Spec: cellv1.BackupSpec{App: "scroll"}}
	if err := k8s.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	completeDatabaseBackup(t, "app-scroll", "scroll-b", "20261119T060000")
	job := &batchv1.Job{}
	eventually(t, func() error {
		return k8s.Get(ctx, client.ObjectKey{Namespace: "app-scroll", Name: jobName("backup", "scroll-b")}, job)
	})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app-scroll", Name: job.Name + "-x", Labels: map[string]string{"job-name": job.Name}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "restic", Image: "restic"}}}}
	if err := k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "scroll-r"}, Spec: cellv1.RestoreSpec{App: "scroll", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	gone(t, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "postgresql.cnpg.io/v1", "kind": "Backup",
		"metadata": map[string]any{"namespace": "app-scroll", "name": "scroll-b"}}})
	// No garbage collector in envtest: play it for the foreground deletion.
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(job), job); err != nil {
			return err
		}
		if job.DeletionTimestamp == nil || !slices.Contains(job.Finalizers, metav1.FinalizerDeleteDependents) {
			return fmt.Errorf("job not deleted in the foreground: %v %v", job.DeletionTimestamp, job.Finalizers)
		}
		job.Finalizers = nil
		return k8s.Update(ctx, job)
	})
	gone(t, job)
	time.Sleep(time.Second)
	if rs := restorePhase(t, "scroll-r", cellv1.RunPending); rs.Status.Message != "waiting for Backup/scroll-b" {
		t.Errorf("restore while the backup's pod runs: %q", rs.Status.Message)
	}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(b), &cellv1.Backup{}); err != nil {
		t.Fatalf("backup gone while its pod runs: %v", err)
	}
	pod.Status.Phase = corev1.PodFailed
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	gone(t, b)
	restorePhase(t, "scroll-r", cellv1.RunRunning)
}

// A stop request reaches restic, which then removes its repository lock;
// the job's shell, as PID 1, would not pass the signal on by itself.
func TestResticGetsStopSignal(t *testing.T) {
	dir := t.TempDir()
	stub := "#!/bin/sh\ntrap 'echo stopped > \"$DIR/signal\"; exit 1' TERM\ntouch \"$DIR/started\"\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(dir, "restic"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", resticPrelude+"restic backup /data\ntouch \"$DIR/went-on\"\n")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error { _, err := os.Stat(filepath.Join(dir, "started")); return err })
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	var err error
	select {
	case err = <-ended:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-ended
		_ = exec.Command("pkill", "-f", dir).Run() // the stub left behind
		t.Fatal("script still runs 5s after the stop request")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 {
		t.Errorf("script ended with %v, want exit 143", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "signal")); err != nil {
		t.Error("restic did not get the stop signal")
	}
	if _, err := os.Stat(filepath.Join(dir, "went-on")); err == nil {
		t.Error("script went on after the stop request")
	}
}

// Removing the app ends a change that is still taking its backup. The
// backup runs under the change's hold, so the change cancels it and keeps
// the app until the backup's job is stopped: until then no reinstall or
// restore may write the volumes the job reads.
func TestRemovalCancelsChangeBackup(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "stave")
	defer removeApp(t, app)
	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "stave-update"},
		Spec: cellv1.ChangeRequestSpec{App: "stave", Requester: cellv1.Requester{Kind: "Human", Name: "alice"},
			Patch:    &apiextensionsv1.JSON{Raw: []byte(`{"version":"2"}`)},
			Approval: cellv1.Approval{Required: true, Decision: "Approved", By: "alice"}},
	}
	if err := k8s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	completeDatabaseBackup(t, "app-stave", "stave-update-pre", "20261119T070000")
	job := &batchv1.Job{}
	eventually(t, func() error {
		return k8s.Get(ctx, client.ObjectKey{Namespace: "app-stave", Name: jobName("backup", "stave-update-pre")}, job)
	})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app-stave", Name: job.Name + "-x", Labels: map[string]string{"job-name": job.Name}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "restic", Image: "restic"}}}}
	if err := k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(ctx, app); err != nil {
		t.Fatal(err)
	}
	// No garbage collector in envtest: play it for the foreground deletion.
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(job), job); err != nil {
			return err
		}
		if job.DeletionTimestamp == nil {
			return fmt.Errorf("backup job not deleted")
		}
		job.Finalizers = nil
		return k8s.Update(ctx, job)
	})
	gone(t, job)
	time.Sleep(time.Second)
	var a cellv1.App
	if err := k8s.Get(ctx, client.ObjectKey{Name: "stave"}, &a); err != nil || a.Annotations[lockKey] != "ChangeRequest/stave-update" {
		t.Fatalf("app let go while the backup's pod runs: %v, held by %q", err, a.Annotations[lockKey])
	}
	if c := changePhase(t, "stave-update", cellv1.RunRunning); !strings.HasPrefix(c.Status.Message, "stopping backup stave-update-pre") {
		t.Errorf("change says %q", c.Status.Message)
	}
	pod.Status.Phase = corev1.PodFailed
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if c := changePhase(t, "stave-update", cellv1.RunFailed); !strings.Contains(c.Status.Message, "was removed while the change ran") {
		t.Errorf("change failed with %q", c.Status.Message)
	}
	gone(t, app)
}

// A database archived plain from before E-67 moves to an encrypted archive
// with its next backup: under the backup's hold, before the base backup,
// which then waits until the instance archives into the new one.
func TestPlainArchiveIsEncryptedByTheNextBackup(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "coffer")
	defer removeApp(t, app)
	cluster, err := get(ctx, gvkCluster, "app-coffer", "db")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"spec": map[string]any{"plugins": []any{archiverPlugin("db-20261101t000000")}}})
	if err := k8s.Patch(ctx, cluster, client.RawPatch(types.MergePatchType, raw)); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(ctx, &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "coffer-b"}, Spec: cellv1.BackupSpec{App: "coffer"}}); err != nil {
		t.Fatal(err)
	}
	var sn string
	eventually(t, func() error {
		c, err := get(ctx, gvkCluster, "app-coffer", "db")
		if err != nil {
			return err
		}
		if sn = serverName(c); !strings.HasPrefix(sn, cryptArchive) {
			return fmt.Errorf("still archived into %s", sn)
		}
		return nil
	})
	var b cellv1.Backup
	if err := k8s.Get(ctx, client.ObjectKey{Name: "coffer-b"}, &b); err != nil || b.Status.Message != switchingArchive {
		t.Fatalf("backup says %q (%v)", b.Status.Message, err)
	}
	time.Sleep(time.Second)
	if _, err := get(ctx, gvkCNPGBackup, "app-coffer", "coffer-b"); !apierrors.IsNotFound(err) {
		t.Fatalf("base backup before the archive settled: %v", err)
	}
	skew.Store(int64(archiveSettle))
	defer skew.Store(0)
	b.Annotations = map[string]string{"test/touch": "1"} // wake the reconciler
	if err := k8s.Update(ctx, &b); err != nil {
		t.Fatal(err)
	}
	done := completeBackup(t, "app-coffer", "coffer-b", "20261126T030000", "snap-v")
	if done.Status.Database == nil || done.Status.Database.ServerName != sn {
		t.Errorf("backup recorded %+v, want archive %s", done.Status.Database, sn)
	}

	// An instance that took the base backup with its old view of the
	// cluster put it into the plain archive: the backup fails instead of
	// recording the encrypted one.
	if err := k8s.Create(ctx, &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "coffer-c"}, Spec: cellv1.BackupSpec{App: "coffer"}}); err != nil {
		t.Fatal(err)
	}
	var pg *unstructured.Unstructured
	eventually(t, func() (err error) { pg, err = get(ctx, gvkCNPGBackup, "app-coffer", "coffer-c"); return err })
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339) // after coffer-b's record
	archived(t, "app-coffer", "db-20261101t000000", end)
	_ = unstructured.SetNestedField(pg.Object, "completed", "status", "phase")
	_ = unstructured.SetNestedField(pg.Object, "stale-1", "status", "backupId")
	_ = unstructured.SetNestedField(pg.Object, end, "status", "stoppedAt")
	_ = unstructured.SetNestedField(pg.Object, pg.GetAnnotations()[annotationCluster], "status", "pluginMetadata", "clusterUID")
	if err := k8s.Status().Update(ctx, pg); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: "coffer-c"}, &b); err != nil {
			return err
		}
		if b.Status.Phase != cellv1.RunFailed || !strings.Contains(b.Status.Message, "is in archive db-20261101t000000, not "+sn) {
			return fmt.Errorf("backup %s: %s", b.Status.Phase, b.Status.Message)
		}
		return nil
	})
	if b.Status.Database != nil {
		t.Errorf("failed backup recorded %+v", b.Status.Database)
	}
}

// The barman-cloud plugin reports a base backup done even when it could
// not record it in its ObjectStore: the backup waits a while for the
// record, then fails instead of naming an archive it may not be in.
func TestUnrecordedBaseBackupFailsAfterAWhile(t *testing.T) {
	app := readyDataApp(t, "almanac")
	defer removeApp(t, app)
	unconfirmedBackupFails(t, "almanac", "almanac-b")
}

// The record the plugin failed to write comes with its next catalog
// check: the waiting backup goes on with the archive it was asked of.
func TestLateRecordedBaseBackupCompletes(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "beacon")
	defer removeApp(t, app)
	cluster, err := get(ctx, gvkCluster, "app-beacon", "db")
	if err != nil {
		t.Fatal(err)
	}
	sn := serverName(cluster)
	b := cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "beacon-b"}, Spec: cellv1.BackupSpec{App: "beacon"}}
	if err := k8s.Create(ctx, &b); err != nil {
		t.Fatal(err)
	}
	var pg *unstructured.Unstructured
	eventually(t, func() (err error) { pg, err = get(ctx, gvkCNPGBackup, "app-beacon", "beacon-b"); return err })
	end := time.Now().UTC().Format(time.RFC3339)
	_ = unstructured.SetNestedField(pg.Object, "completed", "status", "phase")
	_ = unstructured.SetNestedField(pg.Object, "late-1", "status", "backupId")
	_ = unstructured.SetNestedField(pg.Object, end, "status", "stoppedAt")
	_ = unstructured.SetNestedField(pg.Object, pg.GetAnnotations()[annotationCluster], "status", "pluginMetadata", "clusterUID")
	if err := k8s.Status().Update(ctx, pg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // past the backup's 5 s look at its base backup
	if err := k8s.Get(ctx, client.ObjectKey{Name: "beacon-b"}, &b); err != nil {
		t.Fatal(err)
	}
	if b.Status.Phase != cellv1.RunRunning || b.Status.Database != nil {
		t.Fatalf("backup %s (%s) recorded %+v before the record", b.Status.Phase, b.Status.Message, b.Status.Database)
	}
	archived(t, "app-beacon", sn, end)
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: "beacon-b"}, &b); err != nil {
			return err
		}
		if d := b.Status.Database; d == nil || d.ServerName != sn || d.BackupID != "late-1" {
			return fmt.Errorf("backup %s (%s) recorded %+v", b.Status.Phase, b.Status.Message, d)
		}
		return nil
	})
}

// unconfirmedBackupFails runs backup name of app to a base backup the
// plugin reports done but never records, and expects it to wait and then
// fail.
func unconfirmedBackupFails(t *testing.T, app, name string) {
	t.Helper()
	ctx, ns := context.Background(), "app-"+app
	b := cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: cellv1.BackupSpec{App: app}}
	if err := k8s.Create(ctx, &b); err != nil {
		t.Fatal(err)
	}
	var pg *unstructured.Unstructured
	eventually(t, func() (err error) { pg, err = get(ctx, gvkCNPGBackup, ns, name); return err })
	_ = unstructured.SetNestedField(pg.Object, "completed", "status", "phase")
	_ = unstructured.SetNestedField(pg.Object, "unrecorded-1", "status", "backupId")
	_ = unstructured.SetNestedField(pg.Object, time.Now().UTC().Format(time.RFC3339), "status", "stoppedAt")
	_ = unstructured.SetNestedField(pg.Object, pg.GetAnnotations()[annotationCluster], "status", "pluginMetadata", "clusterUID")
	if err := k8s.Status().Update(ctx, pg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // past the backup's 5 s look at its base backup
	if err := k8s.Get(ctx, client.ObjectKey{Name: name}, &b); err != nil {
		t.Fatal(err)
	}
	if b.Status.Phase != cellv1.RunRunning || b.Status.Database != nil {
		t.Fatalf("backup %s (%s) recorded %+v before the wait ended", b.Status.Phase, b.Status.Message, b.Status.Database)
	}
	skew.Store(int64(archiveConfirm))
	defer skew.Store(0)
	b.Annotations = map[string]string{"test/touch": "1"} // wake the reconciler
	if err := k8s.Update(ctx, &b); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKey{Name: name}, &b); err != nil {
			return err
		}
		if b.Status.Phase != cellv1.RunFailed || !strings.Contains(b.Status.Message, "is not confirmed in archive") {
			return fmt.Errorf("backup %s: %s", b.Status.Phase, b.Status.Message)
		}
		return nil
	})
	if b.Status.Database != nil {
		t.Errorf("failed backup recorded %+v", b.Status.Database)
	}
}

// After a switch cancelled before its base backup, with no record of the
// plain archive, an unrecorded base backup still fails after the wait.
func TestUnconfirmedBackupAfterASwitchFails(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "cobble")
	defer removeApp(t, app)
	cluster, err := get(ctx, gvkCluster, "app-cobble", "db")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"spec": map[string]any{"plugins": []any{archiverPlugin("db-20261101t000000")}}})
	if err := k8s.Patch(ctx, cluster, client.RawPatch(types.MergePatchType, raw)); err != nil {
		t.Fatal(err)
	}
	a := cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "cobble-a"}, Spec: cellv1.BackupSpec{App: "cobble"}}
	if err := k8s.Create(ctx, &a); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		c, err := get(ctx, gvkCluster, "app-cobble", "db")
		if err != nil {
			return err
		}
		if !strings.HasPrefix(serverName(c), cryptArchive) {
			return fmt.Errorf("still archived into %s", serverName(c))
		}
		return nil
	})
	// Cancelled before its base backup.
	if err := k8s.Delete(ctx, &a); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(&a), &a); !apierrors.IsNotFound(err) {
			return fmt.Errorf("cancelled backup still there (%v)", err)
		}
		return nil
	})
	unconfirmedBackupFails(t, "cobble", "cobble-b")
}

// An app a failed restore stopped still gets the backup credentials and
// stores an update added (here the encrypted archive of E-67): the next
// restore waits for them.
func TestStoppedAppGetsNewBackupResources(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "cairn")
	defer removeApp(t, app)
	if err := k8s.Create(ctx, &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "cairn-r1"}, Spec: cellv1.RestoreSpec{App: "cairn", Backup: "latest"}}); err != nil {
		t.Fatal(err)
	}
	restorePhase(t, "cairn-r1", cellv1.RunRunning)
	completeJob(t, "app-cairn", jobName("lookup", "cairn-r1"),
		`[{"id":"snap-9","tags":["backup=b9","db-server=db-x","db-backup=id-9"]}]`)
	failJob(t, "app-cairn", jobName("restore", "cairn-r1"), "restic: repository unreachable")
	restorePhase(t, "cairn-r1", cellv1.RunFailed)
	phase(t, "cairn", cellv1.PhaseNeedsAttention)

	// The cell from before the update: no encrypted store, no keys for it.
	store := &unstructured.Unstructured{}
	store.SetGroupVersionKind(gvkObjectStore)
	store.SetNamespace("app-cairn")
	store.SetName(cryptStore)
	if err := k8s.Delete(ctx, store); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		var s corev1.Secret
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: "app-cairn", Name: backupSecret}, &s); err != nil {
			return err
		}
		delete(s.Data, "CRYPT_ACCESS_KEY_ID")
		delete(s.Data, "CRYPT_ACCESS_SECRET_KEY")
		return k8s.Update(ctx, &s)
	})
	target, err := loadTarget(ctx, k8s)
	if err != nil || target == nil {
		t.Fatalf("target %v (%v)", target, err)
	}
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(app), app); err != nil {
			return err
		}
		if app.Status.Phase != cellv1.PhaseNeedsAttention {
			return fmt.Errorf("app %s", app.Status.Phase)
		}
		if ok, err := backupReady(ctx, k8s, app, target); err != nil || !ok {
			return fmt.Errorf("backup resources not in place (%v)", err)
		}
		return nil
	})
}

// The first scheduled backup after the upgrade does not wait for the
// interval while the newest backup lies in a plain archive; once only.
func TestPlainArchiveIsBackedUpAtOnce(t *testing.T) {
	ctx := context.Background()
	app := readyDataApp(t, "inkwell")
	defer removeApp(t, app)
	old := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "inkwell-old"}, Spec: cellv1.BackupSpec{App: "inkwell"}}
	if err := k8s.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(old), old); err != nil {
			return err
		}
		old.Status = cellv1.BackupStatus{Phase: cellv1.RunCompleted, CompletedAt: ptr(metav1.Now()),
			Database: &cellv1.DatabaseBackup{ServerName: "db-20261101t000000", BackupID: "id-old"}}
		return k8s.Status().Update(ctx, old)
	})
	eventually(t, func() error {
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(app), app); err != nil {
			return err
		}
		app.Spec.Backup.Schedule = "daily"
		return k8s.Update(ctx, app)
	})
	scheduled := func() []string {
		var list cellv1.BackupList
		if err := k8s.List(ctx, &list); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, x := range list.Items {
			if x.Spec.App == "inkwell" && x.Spec.Reason == "scheduled" {
				names = append(names, x.Name)
			}
		}
		return names
	}
	eventually(t, func() error {
		if len(scheduled()) == 0 {
			return fmt.Errorf("no backup out of the plain archive")
		}
		return nil
	})
	first := scheduled()[0]
	completeBackup(t, "app-inkwell", first, "20261126T040000", "snap-q1")
	time.Sleep(2 * time.Second)
	if got := scheduled(); len(got) != 1 {
		t.Errorf("scheduled backups %v, want only %s", got, first)
	}
}
