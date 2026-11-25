package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

const (
	checkJobName  = "backup-target-check"
	backupGateway = "backup-gateway"
	// checkSeconds bounds the check: a target that does not answer fails it.
	checkSeconds = 120
)

// annotationTarget on the gateway's pod template restarts it with a new
// target: rclone reads its configuration only at start.
var annotationTarget = cellv1.GroupVersion.Group + "/backup-target"

// BackupTargetReconciler sets the S3 target an admin asked for through the
// API (Secret cell.BackupTargetRequest) once a Job could write to it and
// delete again, and tells what the target is in ConfigMap
// cell.BackupTargetStatus, whoever set it (bootstrap too). A failed check
// leaves the target as it was; that is the rollback.
type BackupTargetReconciler struct {
	client.Client
	// Reader reads the request and the status past the cache: right after
	// a check ended, the cache may still show its request, and a stale
	// read would start the check again.
	Reader      client.Reader
	RcloneImage string
	Now         func() time.Time
}

func (r *BackupTargetReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func system(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: cell.SystemNamespace, Name: name}
}

func (r *BackupTargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	one := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: system(cell.BackupTargetStatus)}}
	})
	named := func(names ...string) builder.Predicates {
		return builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == cell.SystemNamespace && slices.Contains(names, o.GetName())
		}))
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("backup-target").
		Watches(&corev1.Secret{}, one, named(cell.BackupTargetSecret, cell.BackupTargetRequest)).
		Watches(&batchv1.Job{}, one, named(checkJobName)).
		Complete(r)
}

func (r *BackupTargetReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var target corev1.Secret
	if err := r.Get(ctx, system(cell.BackupTargetSecret), &target); err != nil {
		// Bootstrap writes it before the operator runs; the watch brings it.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var before cell.BackupTarget
	var cm corev1.ConfigMap
	if err := r.Reader.Get(ctx, system(cell.BackupTargetStatus), &cm); err == nil {
		if raw := cm.Data[cell.BackupTargetStatusKey]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &before); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	check, done := before.Check, false
	var req corev1.Secret
	err := r.Reader.Get(ctx, system(cell.BackupTargetRequest), &req)
	switch {
	case apierrors.IsNotFound(err):
		// A switch without its request would hold the gateway forever.
		if err := r.clearSwitching(ctx); err != nil {
			return ctrl.Result{}, err
		}
		// A check without its request can never end otherwise.
		if check != nil && check.State == cell.CheckRunning {
			ended := *check
			ended.State, ended.Message, ended.At = cell.CheckFailed, "the request went away before the check ended; ask again", r.now().UTC().Truncate(time.Millisecond)
			check = &ended
			if err := r.deleteJob(ctx); err != nil {
				return ctrl.Result{}, err
			}
		}
	case err != nil:
		return ctrl.Result{}, err
	default:
		if check, done, err = r.check(ctx, &target, &req); err != nil {
			return ctrl.Result{}, err
		}
	}
	status := cell.DescribeBackupTarget(target.Data)
	status.Check = check
	raw, err := json.Marshal(status)
	if err != nil {
		return ctrl.Result{}, err
	}
	apply := corev1ac.ConfigMap(cell.BackupTargetStatus, cell.SystemNamespace).WithData(map[string]string{cell.BackupTargetStatusKey: string(raw)})
	if err := r.Apply(ctx, apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		return ctrl.Result{}, err
	}
	if !done {
		if check != nil && check.State == cell.CheckRunning {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, nil
	}
	// The request goes last: until it is gone, a retry repeats the steps
	// above, each of which may run twice.
	if err := r.deleteJob(ctx); err != nil {
		return ctrl.Result{}, err
	}
	uid := req.UID
	return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, &req, client.Preconditions{UID: &uid}))
}

func (r *BackupTargetReconciler) clearSwitching(ctx context.Context) error {
	l, err := readLock(ctx, r.Reader)
	if err != nil || l.switching == "" {
		return err
	}
	l.switching = ""
	return l.write(ctx, r)
}

func (r *BackupTargetReconciler) deleteJob(ctx context.Context) error {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: checkJobName}}
	return client.IgnoreNotFound(r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

// check runs the check of a request and, once it passed, sets the target
// (target is updated in place). done: the request is answered.
func (r *BackupTargetReconciler) check(ctx context.Context, target, req *corev1.Secret) (*cell.BackupCheck, bool, error) {
	d := func(k string) string { return string(req.Data[k]) }
	prefix := d("prefix")
	if prefix == "" {
		prefix = string(target.Data["prefix"])
	}
	if prefix == "" {
		cfg, err := cell.LoadConfig(ctx, r)
		if err != nil {
			return nil, false, err
		}
		prefix = cfg.Domain
	}
	root := d("bucket") + "/" + prefix
	running := &cell.BackupCheck{State: cell.CheckRunning, Target: d("endpoint") + "/" + root, By: d("requester")}
	ended := func(state, msg string) (*cell.BackupCheck, bool, error) {
		c := *running
		// After the time it was asked at, also when the API's clock is
		// ahead: the console tells checks apart by it.
		c.State, c.Message, c.At = state, msg, r.now().UTC().Truncate(time.Millisecond)
		if !c.At.After(running.At) {
			c.At = running.At.Add(time.Millisecond)
		}
		return &c, true, nil
	}
	at, err := time.Parse(time.RFC3339Nano, d("requestedAt"))
	if err != nil {
		return ended(cell.CheckFailed, "the request has no valid time; ask again")
	}
	running.At = at
	conf, err := cell.S3RcloneConf(d("endpoint"), d("accessKey"), d("secretKey"), d("region"))
	if err != nil || d("bucket") == "" {
		return ended(cell.CheckFailed, "the request is incomplete; ask again")
	}

	job := checkJob(r.RcloneImage, root)
	if ok, err := ensureChild(ctx, r, job, string(req.UID)); err != nil || !ok {
		return running, false, err
	}
	finished, out, err := jobResult(ctx, r, job)
	if !finished {
		return running, false, err
	}
	if err != nil {
		return ended(cell.CheckFailed, checkMessage(job, out, d("accessKey"), d("secretKey")))
	}

	waiting := func(msg string) (*cell.BackupCheck, bool, error) {
		w := *running
		w.Message = "the check passed; " + msg
		return &w, false, nil
	}
	// Passed. The target switches only under the gateway's lock (see
	// gateway_lock.go), and each step below may run again after a crash.
	uid := string(req.UID)
	l, err := readLock(ctx, r.Reader)
	if err != nil {
		return nil, false, err
	}
	switched := string(target.Data["rclone.conf"]) == conf && string(target.Data["root"]) == root
	if l.switching != uid && !(switched && l.switching == "") {
		// Only one request exists: another request's switching is stale.
		// Taken first, so no new run starts: a steady stream of runs cannot
		// hold the switch off.
		l.switching = uid
		if err := l.write(ctx, r); err != nil {
			return nil, false, err
		}
	}
	if l.switching == uid {
		// Runs that started before wait to the end.
		var live []string
		for _, u := range l.users {
			in, err := usesGateway(ctx, r.Reader, u)
			if err != nil {
				return nil, false, err
			}
			if in {
				live = append(live, u)
			}
		}
		if len(live) != len(l.users) {
			l.users = live
			if err := l.write(ctx, r); err != nil {
				return nil, false, err
			}
		}
		if len(live) > 0 {
			return waiting(fmt.Sprintf("switching once %d running backups, restores or changes are done", len(live)))
		}
		// The target the gateway and bootstrap read, with the password and
		// gateway key kept (they derive the apps' keys and encrypt backups).
		if !switched {
			if target.Data == nil {
				target.Data = map[string][]byte{}
			}
			target.Data["rclone.conf"], target.Data["root"], target.Data["prefix"] = []byte(conf), []byte(root), []byte(prefix)
			target.Data["localVolume"] = []byte("emptyDir: {}")
			if err := r.Update(ctx, target); err != nil {
				return nil, false, err
			}
		}
		sum, err := r.restartGateway(ctx, conf+"\n"+root)
		if err != nil {
			return nil, false, err
		}
		if ok, err := r.gatewayRolledOut(ctx, sum); err != nil || !ok {
			if err != nil {
				return nil, false, err
			}
			return waiting("the backup gateway restarts with the new target")
		}
		// The first backups go in before the lock opens; they wait for it.
		if err := r.backUpAll(ctx, uid); err != nil {
			return nil, false, err
		}
		l.switching = ""
		if err := l.write(ctx, r); err != nil {
			return nil, false, err
		}
	} else if err := r.backUpAll(ctx, uid); err != nil {
		// Switched before a restart: the first backups and the request are left.
		return nil, false, err
	}
	return ended(cell.CheckPassed, "")
}

// backUpAll starts a backup of every app with data at once: the database
// archive of the new target starts with a base backup, and until one is
// there the new target restores nothing. They wait for the gateway's
// lock to open. Named after the request, so a retry adds none.
func (r *BackupTargetReconciler) backUpAll(ctx context.Context, request string) error {
	if len(request) > 8 {
		request = request[:8]
	}
	var apps cellv1.AppList
	if err := r.List(ctx, &apps); err != nil {
		return err
	}
	for i := range apps.Items {
		app := &apps.Items[i]
		if !hasData(app) || !app.DeletionTimestamp.IsZero() {
			continue
		}
		if err := createOnce(ctx, r, &cellv1.Backup{
			ObjectMeta: metav1.ObjectMeta{
				Name:   app.Name + "-target-" + request,
				Labels: map[string]string{cellv1.LabelApp: app.Name},
			},
			Spec: cellv1.BackupSpec{App: app.Name, Reason: "first backup to the new backup target"},
		}); err != nil {
			return err
		}
	}
	return nil
}

// restartGateway rolls the gateway when its target changed and returns
// the target's hash, which the new pods carry.
func (r *BackupTargetReconciler) restartGateway(ctx context.Context, target string) (string, error) {
	var gw appsv1.Deployment
	if err := r.Get(ctx, system(backupGateway), &gw); err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(target))
	sum := hex.EncodeToString(h[:])[:16]
	if gw.Spec.Template.Annotations[annotationTarget] == sum {
		return sum, nil
	}
	patch := client.MergeFrom(gw.DeepCopy())
	if gw.Spec.Template.Annotations == nil {
		gw.Spec.Template.Annotations = map[string]string{}
	}
	gw.Spec.Template.Annotations[annotationTarget] = sum
	return sum, r.Patch(ctx, &gw, patch)
}

// gatewayRolledOut tells, past the cache, whether every gateway pod runs
// the target with this hash: no pod of the old one serves any more.
func (r *BackupTargetReconciler) gatewayRolledOut(ctx context.Context, sum string) (bool, error) {
	var gw appsv1.Deployment
	if err := r.Reader.Get(ctx, system(backupGateway), &gw); err != nil {
		return false, err
	}
	want := int32(1)
	if gw.Spec.Replicas != nil {
		want = *gw.Spec.Replicas
	}
	st := gw.Status
	return gw.Spec.Template.Annotations[annotationTarget] == sum && st.ObservedGeneration >= gw.Generation &&
		st.Replicas == want && st.UpdatedReplicas == want && st.ReadyReplicas == want && st.AvailableReplicas == want, nil
}

// checkScript writes a small object under the target's root and deletes
// it: that fails on a wrong key, a missing bucket and a key that may only
// read. ROOT is an env value, never part of the script.
const checkScript = `set -eu
echo "written by the cell to check its backup target; safe to delete" | rclone rcat "target:$ROOT/.cell-check"
rclone deletefile "target:$ROOT/.cell-check"`

// checkJob checks the target of the request with rclone. The remote is
// configured from env, the keys from the request secret, so neither the
// Job nor a file holds them.
func checkJob(image, root string) *batchv1.Job {
	fromRequest := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: cell.BackupTargetRequest}, Key: key}}}
	}
	l := map[string]string{"app.kubernetes.io/name": checkJobName}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: checkJobName, Namespace: cell.SystemNamespace, Labels: l},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr(int32(0)),
			ActiveDeadlineSeconds: ptr(int64(checkSeconds)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: l},
				Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{
					Name:    "rclone",
					Image:   image,
					Command: []string{"sh", "-c", checkScript},
					Env: []corev1.EnvVar{
						{Name: "ROOT", Value: root},
						{Name: "RCLONE_CONFIG_TARGET_TYPE", Value: "s3"},
						{Name: "RCLONE_CONFIG_TARGET_PROVIDER", Value: "Other"},
						fromRequest("RCLONE_CONFIG_TARGET_ENDPOINT", "endpoint"),
						fromRequest("RCLONE_CONFIG_TARGET_REGION", "region"),
						fromRequest("RCLONE_CONFIG_TARGET_ACCESS_KEY_ID", "accessKey"),
						fromRequest("RCLONE_CONFIG_TARGET_SECRET_ACCESS_KEY", "secretKey"),
						// A key that may not create buckets must pass; a missing
						// bucket then fails the write.
						{Name: "RCLONE_CONFIG_TARGET_NO_CHECK_BUCKET", Value: "true"},
						{Name: "RCLONE_RETRIES", Value: "1"},
						{Name: "RCLONE_LOW_LEVEL_RETRIES", Value: "1"},
						{Name: "RCLONE_CONTIMEOUT", Value: "15s"},
						{Name: "RCLONE_TIMEOUT", Value: "30s"},
					},
					// The failure is in the logs; the operator reads it from the
					// pod's status, without access to logs.
					TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				}}},
			},
		},
	}
}

var logPrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} (?:[A-Z]+ *: *)?`)

// checkMessage is why a check failed in one line: the last line rclone
// logged, without its time, the keys blanked out.
func checkMessage(job *batchv1.Job, out string, secrets ...string) string {
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
		}
	}
	line = logPrefix.ReplaceAllString(line, "")
	for _, s := range secrets {
		if s != "" {
			line = strings.ReplaceAll(line, s, "***")
		}
	}
	if line == "" {
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Reason == batchv1.JobReasonDeadlineExceeded {
				return "no answer within 2 minutes: is the endpoint right and reachable from the cell?"
			}
		}
		return "the check failed without saying why"
	}
	if r := []rune(line); len(r) > 300 {
		line = string(r[:300]) + "..."
	}
	return line
}
