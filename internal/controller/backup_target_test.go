package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// An admin's target is checked by a Job and used only once the Job could
// write to it; a failed check leaves the target as it was. The status
// tells both, without a secret.
func TestBackupTargetCheck(t *testing.T) {
	ctx := context.Background()
	request := func(uid, secretKey string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetRequest, UID: types.UID(uid)},
			Data: map[string][]byte{"endpoint": []byte("https://fsn1.your-objectstorage.com"), "region": []byte("fsn1"), "bucket": []byte("bk"),
				"accessKey": []byte("AKID"), "secretKey": []byte(secretKey), "requester": []byte("ann"), "requestedAt": []byte("2026-11-25T10:00:00.5Z")}}
	}
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetSecret},
		Data: map[string][]byte{"password": []byte("pw"), "gatewayKey": []byte("gk"), "prefix": []byte("cell.test"), "rclone.conf": nil, "root": nil}}
	gateway := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: backupGateway}}
	c := fake.NewClientBuilder().WithScheme(k8s.Scheme()).WithObjects(target, gateway, request("req-1", "s3cr3t")).Build()
	now := time.Date(2026, 11, 25, 10, 0, 30, 0, time.UTC)
	r := &BackupTargetReconciler{Client: c, Reader: c, RcloneImage: "rclone:test", Now: func() time.Time { return now }}
	answered := "" // the request the status answers; the installer tells its own result by it
	step := func() cell.BackupTarget {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
			t.Fatal(err)
		}
		var cm corev1.ConfigMap
		if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: cell.BackupTargetStatus}, &cm); err != nil {
			t.Fatal(err)
		}
		answered = cm.Data[cell.BackupTargetStatusRequestKey]
		raw := cm.Data[cell.BackupTargetStatusKey]
		if strings.Contains(raw, "s3cr3t") || strings.Contains(raw, "wrong") {
			t.Fatalf("the status holds a secret key: %s", raw)
		}
		var s cell.BackupTarget
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	finish := func(ok bool, message string) {
		t.Helper()
		var job batchv1.Job
		if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: checkJobName}, &job); err != nil {
			t.Fatal(err)
		}
		cond, code := batchv1.JobComplete, int32(0)
		if !ok {
			cond, code = batchv1.JobFailed, 1
		}
		job.Status.Conditions = []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue}}
		if err := c.Status().Update(ctx, &job); err != nil {
			t.Fatal(err)
		}
		// The fake client collects no garbage: the earlier Job's pod goes by hand.
		_ = c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "check"}})
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "check",
			Labels: map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "rclone",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Message: message}}}}}}
		if err := c.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}

	s := step()
	if s.Configured || s.Check == nil || s.Check.State != cell.CheckRunning || s.Check.By != "ann" ||
		!s.Check.At.Equal(time.Date(2026, 11, 25, 10, 0, 0, 5e8, time.UTC)) || s.Check.Target != "https://fsn1.your-objectstorage.com/bk/cell.test" {
		t.Fatalf("checking: %+v %+v", s, s.Check)
	}
	finish(true, "")
	// The target is switched, but the check holds the gateway's lock until
	// the gateway runs it.
	if s = step(); s.Check.State != cell.CheckRunning || !strings.Contains(s.Check.Message, "restarts") || !s.Configured {
		t.Fatalf("restarting: %+v %+v", s, s.Check)
	}
	rollOut(t, c)
	s = step()
	if !s.Configured || s.Kind != "s3" || s.Bucket != "bk" || s.Region != "fsn1" || s.AccessKey != "AKID" || s.Check.State != cell.CheckPassed || !s.Check.At.Equal(now) {
		t.Fatalf("passed: %+v %+v", s, s.Check)
	}
	if answered != "req-1" {
		t.Errorf("passed answers %q", answered)
	}
	var got corev1.Secret
	if err := c.Get(ctx, client.ObjectKeyFromObject(target), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Data["rclone.conf"]), "\nsecret_access_key = s3cr3t\n") || string(got.Data["root"]) != "bk/cell.test" ||
		string(got.Data["password"]) != "pw" || string(got.Data["gatewayKey"]) != "gk" || string(got.Data["localVolume"]) != "emptyDir: {}" {
		t.Errorf("target: %q", got.Data)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(gateway), gateway); err != nil || gateway.Spec.Template.Annotations[annotationTarget] == "" {
		t.Errorf("gateway not restarted: %v %v", err, gateway.Spec.Template.Annotations)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: cell.BackupTargetRequest}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("request left behind: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: checkJobName}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Errorf("job left behind: %v", err)
	}

	// Right after, the cache may still hold the request: that starts no
	// second check.
	stale := request("req-1", "s3cr3t")
	cached := interceptor.NewClient(c, interceptor.Funcs{Get: func(ctx context.Context, w client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if key.Name == cell.BackupTargetRequest {
			stale.DeepCopyInto(o.(*corev1.Secret))
			return nil
		}
		return w.Get(ctx, key, o, opts...)
	}})
	r.Client = cached
	if s := step(); s.Check.State != cell.CheckPassed {
		t.Errorf("stale cache: %+v", s.Check)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: checkJobName}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Errorf("stale cache started a job: %v", err)
	}
	r.Client = c

	// A wrong key: the check fails with rclone's reason, the target stays.
	if err := c.Create(ctx, request("req-2", "wrong")); err != nil {
		t.Fatal(err)
	}
	step()
	finish(false, "2026/11/25 10:00:05 NOTICE: Config file not found\n2026/11/25 10:00:06 CRITICAL: Failed to rcat: InvalidAccessKeyId: AKID wrong is unknown\n")
	s = step()
	if s.Check.State != cell.CheckFailed || s.Check.Message != "Failed to rcat: InvalidAccessKeyId: *** *** is unknown" || !s.Configured {
		t.Fatalf("failed: %+v %+v", s, s.Check)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(target), &got); err != nil || !strings.Contains(string(got.Data["rclone.conf"]), "s3cr3t") {
		t.Errorf("a failed check changed the target: %v %q", err, got.Data["rclone.conf"])
	}
	// Another reconcile keeps the result.
	if s = step(); s.Check.State != cell.CheckFailed || answered != "req-2" {
		t.Errorf("result lost: %+v %q", s.Check, answered)
	}
	// A check whose request went away ends instead of running forever.
	if err := c.Create(ctx, request("req-3", "s3cr3t")); err != nil {
		t.Fatal(err)
	}
	step()
	if err := c.Delete(ctx, request("req-3", "")); err != nil {
		t.Fatal(err)
	}
	if s = step(); s.Check.State != cell.CheckFailed || !strings.Contains(s.Check.Message, "went away") || answered != "req-3" {
		t.Errorf("orphaned check: %+v %q", s.Check, answered)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: checkJobName}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Errorf("orphaned check's job left behind: %v", err)
	}

	// Plain http only on a private network, asked for on purpose (E-68):
	// whoever wrote the request, a refused one starts no Job.
	plain := func(uid, endpoint, private string) *corev1.Secret {
		q := request(uid, "s3cr3t")
		q.Data["endpoint"], q.Data["privateNetwork"] = []byte(endpoint), []byte(private)
		return q
	}
	for i, q := range []*corev1.Secret{plain("req-4", "http://10.0.0.5:9000", ""), plain("req-5", "http://minio.lan:9000", "true")} {
		if err := c.Create(ctx, q); err != nil {
			t.Fatal(err)
		}
		if s = step(); s.Check.State != cell.CheckFailed || !strings.Contains(s.Check.Message, "http") {
			t.Errorf("plain http %d: %+v", i, s.Check)
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: checkJobName}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
			t.Errorf("plain http %d started a check: %v", i, err)
		}
	}
	if err := c.Create(ctx, plain("req-6", "http://10.0.0.5:9000", "true")); err != nil {
		t.Fatal(err)
	}
	if s = step(); s.Check.State != cell.CheckRunning {
		t.Errorf("private http: %+v", s.Check)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "cell-system", Name: checkJobName}, &batchv1.Job{}); err != nil {
		t.Errorf("private http started no check: %v", err)
	}
}

// The check Job takes its keys from the request secret, never as values,
// and leaves its failure where the operator can read it.
func TestCheckJob(t *testing.T) {
	job := checkJob("rclone:test", `bk/cell.test`)
	ctr := job.Spec.Template.Spec.Containers[0]
	if *job.Spec.BackoffLimit != 0 || job.Spec.ActiveDeadlineSeconds == nil || ctr.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError ||
		ctr.Command[0] != "sh" || strings.Contains(ctr.Command[2], "bk/") {
		t.Errorf("job: %+v", job.Spec)
	}
	for _, e := range ctr.Env {
		if strings.Contains(e.Name, "KEY") && (e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef.Name != cell.BackupTargetRequest) {
			t.Errorf("%s is not from the request secret", e.Name)
		}
	}
	failed := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonDeadlineExceeded}}}}
	if m := checkMessage(failed, ""); !strings.HasPrefix(m, "no answer within") {
		t.Errorf("deadline: %q", m)
	}
}

// A passed check waits with the switch while a backup runs: that backup
// would write half to each target.
func TestBackupTargetSwitchWaitsForRuns(t *testing.T) {
	ctx := context.Background()
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetSecret},
		Data: map[string][]byte{"password": []byte("pw"), "gatewayKey": []byte("gk"), "prefix": []byte("cell.test")}}
	req := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetRequest, UID: "req-1"},
		Data: map[string][]byte{"endpoint": []byte("https://s3.test"), "region": []byte("us-east-1"), "bucket": []byte("bk"),
			"accessKey": []byte("AKID"), "secretKey": []byte("sk"), "requester": []byte("ann"), "requestedAt": []byte("2026-11-25T10:00:00Z")}}
	running := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "notes-1"}, Spec: cellv1.BackupSpec{App: "notes"},
		Status: cellv1.BackupStatus{Phase: cellv1.RunRunning}}
	gateway := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: backupGateway}}
	notes := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "notes"}}
	notes.Spec.Needs.Volumes = []cellv1.Volume{{Name: "data"}}
	plain := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}
	lock := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: gatewayLock}, Data: map[string]string{lockUsers: `["backup/notes-1"]`}}
	c := fake.NewClientBuilder().WithScheme(k8s.Scheme()).WithObjects(target, req, running, gateway, notes, plain, lock).WithStatusSubresource(&cellv1.Backup{}).Build()
	r := &BackupTargetReconciler{Client: c, Reader: c, RcloneImage: "rclone:test"}
	reconcileStatus := func() cell.BackupCheck {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
			t.Fatal(err)
		}
		var cm corev1.ConfigMap
		if err := c.Get(ctx, system(cell.BackupTargetStatus), &cm); err != nil {
			t.Fatal(err)
		}
		var s cell.BackupTarget
		if err := json.Unmarshal([]byte(cm.Data[cell.BackupTargetStatusKey]), &s); err != nil {
			t.Fatal(err)
		}
		return *s.Check
	}
	reconcileStatus() // creates the job
	var job batchv1.Job
	if err := c.Get(ctx, system(checkJobName), &job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "check",
		Labels: map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}}}); err != nil {
		t.Fatal(err)
	}

	if ch := reconcileStatus(); ch.State != cell.CheckRunning || !strings.Contains(ch.Message, "1 running") {
		t.Fatalf("while a backup runs: %+v", ch)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(target), target); err != nil || len(target.Data["rclone.conf"]) != 0 {
		t.Fatalf("switched while a backup runs: %v %q", err, target.Data["rclone.conf"])
	}
	running.Status.Phase = cellv1.RunCompleted
	if err := c.Status().Update(ctx, running); err != nil {
		t.Fatal(err)
	}
	if ch := reconcileStatus(); ch.State != cell.CheckRunning || !strings.Contains(ch.Message, "restarts") {
		t.Fatalf("after the backup: %+v", ch)
	}
	rollOut(t, c)
	if ch := reconcileStatus(); ch.State != cell.CheckPassed {
		t.Fatalf("after the restart: %+v", ch)
	}
	// The new target restores only once each app has a backup there.
	var backups cellv1.BackupList
	if err := c.List(ctx, &backups); err != nil {
		t.Fatal(err)
	}
	var first []string
	for _, b := range backups.Items {
		if b.Spec.Reason == "first backup to the new backup target" {
			first = append(first, b.Name)
		}
	}
	if len(first) != 1 || first[0] != "notes-target-req-1" {
		t.Fatalf("first backups at the new target: %v", first)
	}
}

// A backup does not start while the target switches, holds the gateway's
// lock while it runs and lets go when it ends.
func TestBackupWaitsForTargetSwitch(t *testing.T) {
	ctx := context.Background()
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetSecret},
		Data: map[string][]byte{"password": []byte("pw"), "gatewayKey": []byte("gk"), "rclone.conf": []byte("[target]\ntype = local\n")}}
	lock := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: gatewayLock}, Data: map[string]string{lockSwitching: "req-1"}}
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "quill"}}
	b := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "quill-1", Finalizers: []string{backupFinalizer}}, Spec: cellv1.BackupSpec{App: "quill"}}
	c := fake.NewClientBuilder().WithScheme(k8s.Scheme()).WithObjects(target, lock, app, b).WithStatusSubresource(&cellv1.Backup{}).Build()
	r := &BackupReconciler{Client: c, Reader: c}
	phase := func() (cellv1.RunPhase, string) {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)}); err != nil {
			t.Fatal(err)
		}
		var got cellv1.Backup
		if err := c.Get(ctx, client.ObjectKeyFromObject(b), &got); err != nil {
			t.Fatal(err)
		}
		return got.Status.Phase, got.Status.Message
	}
	if p, m := phase(); p != cellv1.RunPending || m != waitingForSwitch || users(t, c) != "[]" {
		t.Errorf("while switching: %s %q, users %s", p, m, users(t, c))
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(lock), lock); err != nil {
		t.Fatal(err)
	}
	lock.Data[lockSwitching] = ""
	if err := c.Update(ctx, lock); err != nil {
		t.Fatal(err)
	}
	if p, _ := phase(); p != cellv1.RunRunning || users(t, c) != `["backup/quill-1"]` {
		t.Errorf("after the switch: %s, users %s", p, users(t, c))
	}
	var got cellv1.Backup
	if err := c.Get(ctx, client.ObjectKeyFromObject(b), &got); err != nil {
		t.Fatal(err)
	}
	got.Status.Phase = cellv1.RunCompleted
	if err := c.Status().Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if phase(); users(t, c) != "[]" {
		t.Errorf("ended backup still listed: %s", users(t, c))
	}
}

// A change holds the gateway's lock from its start: it does not start
// while the target switches.
func TestChangeWaitsForTargetSwitch(t *testing.T) {
	ctx := context.Background()
	lock := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: gatewayLock}, Data: map[string]string{lockSwitching: "req-1"}}
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "quill"}}
	cr := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "quill-up", Finalizers: []string{changeFinalizer}},
		Spec: cellv1.ChangeRequestSpec{App: "quill", Action: actionUpdate}}
	c := fakeClient(k8s.Scheme(), lock, app, cr)
	r := &ChangeReconciler{Client: c, Reader: c}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil || cr.Status.Phase != cellv1.RunPending || cr.Status.Message != waitingForSwitch {
		t.Errorf("change while switching: %v %s %q", err, cr.Status.Phase, cr.Status.Message)
	}
}

// The switch waits for every listed user that still runs, drops the ones
// left behind, holds the lock until the gateway restarted, then opens it.
func TestSwitchWaitsForEveryUser(t *testing.T) {
	ctx := context.Background()
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetSecret},
		Data: map[string][]byte{"password": []byte("pw"), "gatewayKey": []byte("gk"), "prefix": []byte("cell.test")}}
	req := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetRequest, UID: "req-9"},
		Data: map[string][]byte{"endpoint": []byte("https://s3.test"), "bucket": []byte("bk"), "accessKey": []byte("AKID"), "secretKey": []byte("sk"),
			"requester": []byte("ann"), "requestedAt": []byte("2026-11-25T10:00:00Z")}}
	lock := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: gatewayLock},
		Data: map[string]string{lockUsers: `["backup/b","backup/gone","change/cr","restore/done","restore/rs"]`}}
	b := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Status: cellv1.BackupStatus{Phase: cellv1.RunRunning}}
	rs := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "rs"}, Status: cellv1.RestoreStatus{Phase: cellv1.RunPending}}
	done := &cellv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: "done"}, Status: cellv1.RestoreStatus{Phase: cellv1.RunCompleted}}
	cr := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "cr"}, Status: cellv1.ChangeRequestStatus{Phase: cellv1.RunRunning}}
	gateway := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: backupGateway}}
	c := fake.NewClientBuilder().WithScheme(k8s.Scheme()).WithObjects(target, req, lock, b, rs, done, cr, gateway).
		WithStatusSubresource(&cellv1.Backup{}, &cellv1.Restore{}, &cellv1.ChangeRequest{}).Build()
	r := &BackupTargetReconciler{Client: c, Reader: c, RcloneImage: "rclone:test"}
	step := func() {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	step()
	passJob(t, c)
	step()
	// The switch closes the lock at once and waits for the runs inside.
	if got := users(t, c); got != `["backup/b","change/cr","restore/rs"]` || switching(t, c) != "req-9" {
		t.Fatalf("users while they run: %s, switching %q", got, switching(t, c))
	}
	if why, err := acquireGateway(ctx, c, c, "backup/late"); err != nil || why != waitingForSwitch {
		t.Errorf("a run started while the switch waits: %q %v", why, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(target), target); err != nil || len(target.Data["rclone.conf"]) != 0 {
		t.Fatalf("switched while users run: %v", err)
	}
	b.Status.Phase, rs.Status.Phase, cr.Status.Phase = cellv1.RunCompleted, cellv1.RunFailed, cellv1.RunRolledBack
	for _, o := range []client.Object{b, rs, cr} {
		if err := c.Status().Update(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	step()
	if switching(t, c) != "req-9" {
		t.Fatalf("switch did not take the lock: %q", switching(t, c))
	}
	if why, err := acquireGateway(ctx, c, c, "backup/new"); err != nil || why != waitingForSwitch {
		t.Errorf("a run acquired during the switch: %q %v", why, err)
	}
	step() // the gateway has not restarted yet
	if switching(t, c) != "req-9" {
		t.Fatalf("lock opened before the restart")
	}
	rollOut(t, c)
	step()
	if switching(t, c) != "" || users(t, c) != "[]" {
		t.Errorf("lock after the switch: %q %s", switching(t, c), users(t, c))
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(target), target); err != nil || !strings.Contains(string(target.Data["rclone.conf"]), "endpoint = https://s3.test") {
		t.Errorf("target not switched: %v", err)
	}
}

// Of two sides writing the lock from the same read, one gets a conflict
// and retries; a switching left by a request that is gone is cleared.
func TestGatewayLockConflictsAndStaleSwitch(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.Scheme()).Build()
	a, err := readLock(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := readLock(ctx, c)
	a.users = []string{"backup/a"}
	if err := a.write(ctx, c); err != nil {
		t.Fatal(err)
	}
	b.switching = "req-1"
	if err := b.write(ctx, c); !apierrors.IsAlreadyExists(err) {
		t.Errorf("second create: %v", err)
	}
	old, _ := readLock(ctx, c)
	stale := old.cm.DeepCopy()
	if why, err := acquireGateway(ctx, c, c, "restore/r"); err != nil || why != "" {
		t.Fatal(why, err)
	}
	old.switching = "req-1"
	if err := old.write(ctx, c); !apierrors.IsConflict(err) {
		t.Errorf("switch from a stale read: %v", err)
	}
	staleReader := interceptor.NewClient(c, interceptor.Funcs{Get: func(ctx context.Context, w client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if key.Name == gatewayLock {
			stale.DeepCopyInto(o.(*corev1.ConfigMap))
			return nil
		}
		return w.Get(ctx, key, o, opts...)
	}})
	if _, err := acquireGateway(ctx, c, staleReader, "change/x"); !apierrors.IsConflict(err) {
		t.Errorf("acquire from a stale read: %v", err)
	}
	if got := users(t, c); got != `["backup/a","restore/r"]` {
		t.Errorf("users: %s", got)
	}

	// A switch whose request went away.
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetSecret}, Data: map[string][]byte{"password": []byte("pw")}}
	if err := c.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	cur, _ := readLock(ctx, c)
	cur.switching = "gone"
	if err := cur.write(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := (&BackupTargetReconciler{Client: c, Reader: c}).Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	if switching(t, c) != "" || users(t, c) != `["backup/a","restore/r"]` {
		t.Errorf("stale switch kept: %q %s", switching(t, c), users(t, c))
	}
}

func readLockData(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), system(gatewayLock), &cm); err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return cm.Data
}

func users(t *testing.T, c client.Client) string {
	t.Helper()
	if u := readLockData(t, c)[lockUsers]; u != "" {
		return u
	}
	return "[]"
}

func switching(t *testing.T, c client.Client) string {
	t.Helper()
	return readLockData(t, c)[lockSwitching]
}

// passJob lets the check Job pass, as the Job controller and kubelet would.
func passJob(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	var job batchv1.Job
	if err := c.Get(ctx, system(checkJobName), &job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "check-" + job.Name,
		Labels: map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}}}); err != nil {
		t.Fatal(err)
	}
}

// rollOut finishes the gateway's rollout, as the Deployment controller would.
func rollOut(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	var gw appsv1.Deployment
	if err := c.Get(ctx, system(backupGateway), &gw); err != nil {
		t.Fatal(err)
	}
	gw.Status = appsv1.DeploymentStatus{ObservedGeneration: gw.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if err := c.Status().Update(ctx, &gw); err != nil {
		t.Fatal(err)
	}
}

// A switch that closed the lock while a change holds it lets that change's
// own backup and rollback in, else each would wait for the other forever;
// also for a change whose name is too long for a label.
func TestSwitchAdmitsRunsOfHoldingChange(t *testing.T) {
	ctx := context.Background()
	long := "update-notes-" + strings.Repeat("x", 70)
	lock := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: gatewayLock},
		Data: map[string]string{lockUsers: `["change/` + long + `"]`, lockSwitching: "req-1"}}
	cr := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: long}, Spec: cellv1.ChangeRequestSpec{App: "notes"}}
	cr.Status.Backup, cr.Status.Restore = "cr-pre", "cr-back"
	other := &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "other"}, Spec: cellv1.ChangeRequestSpec{App: "notes"}}
	other.Status.Backup = "other-pre"
	c := fake.NewClientBuilder().WithScheme(k8s.Scheme()).WithObjects(lock, cr, other).
		WithStatusSubresource(&cellv1.ChangeRequest{}).Build()
	for _, tc := range []struct{ user, want string }{
		{"backup/other-pre", waitingForSwitch}, // its change is not inside
		{"backup/cr-x", waitingForSwitch},      // no change's own
		{"restore/cr-pre", waitingForSwitch},   // the backup's name, as a restore
		{"backup/cr-pre", ""},
		{"restore/cr-back", ""},
	} {
		if why, err := acquireGateway(ctx, c, c, tc.user); err != nil || why != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.user, why, err, tc.want)
		}
	}
	if got := users(t, c); got != `["backup/cr-pre","change/`+long+`","restore/cr-back"]` {
		t.Errorf("users: %s", got)
	}
}
