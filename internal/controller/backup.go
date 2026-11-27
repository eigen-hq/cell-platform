package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// Backups leave the cell: databases through the CloudNativePG barman-cloud
// plugin (base backups plus continuous WAL), volumes through restic. Both
// go through the backup gateway (deploy/base/backup-gateway.yaml), which
// alone holds the target: an app's key reaches only <root>/apps/<app>/ of
// it, where the "buckets" postgres and volumes are directories. The restic
// snapshot of a backup carries the database backup's coordinates as tags,
// so a fresh cell finds everything from the target alone.
//
// restic encrypts the volumes itself. The database archive is encrypted by
// the gateway (E-67): an app's second key, "<app>.db", reaches an rclone
// crypt remote at <root>/apps/<app>/crypt, keyed by the backup password
// alone. Archives named "crypt-..." live there (ObjectStore backup-crypt),
// older "db-..." ones stay plain where they are (ObjectStore backup).

const (
	gatewayRegion  = "us-east-1" // the gateway takes any; clients want one
	backupSecret   = "backup-s3"
	objectStore    = "backup"
	cryptStore     = "backup-crypt"
	cryptArchive   = "crypt-"
	barmanPlugin   = "barman-cloud.cloudnative-pg.io"
	resticDataRoot = "/data"
)

var (
	gvkObjectStore = schema.GroupVersionKind{Group: "barmancloud.cnpg.io", Version: "v1", Kind: "ObjectStore"}
	gvkCNPGBackup  = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Backup"}
)

// gatewayURL is the backup gateway's S3 endpoint.
const gatewayURL = "http://backup-gateway." + cell.SystemNamespace + ".svc:8080"

// backupTarget is what apps need of the cell-system/backup-target secret
// written by bootstrap; the target itself stays with the gateway.
type backupTarget struct {
	Password, GatewayKey string
}

// loadTarget returns nil when the cell has no backup target: no secret, or
// one without a target yet (bootstrap always writes the secret, an admin
// may set the target later).
func loadTarget(ctx context.Context, c client.Reader) (*backupTarget, error) {
	var s corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: cell.SystemNamespace, Name: cell.BackupTargetSecret}, &s)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d := func(k string) string { return string(s.Data[k]) }
	if strings.TrimSpace(d("rclone.conf")) == "" {
		return nil, nil
	}
	t := &backupTarget{Password: d("password"), GatewayKey: d("gatewayKey")}
	if t.Password == "" || t.GatewayKey == "" {
		return nil, fmt.Errorf("backup target is incomplete (password and gatewayKey are required)")
	}
	return t, nil
}

// appSecret is an app's key at the gateway; the gateway's auth proxy
// derives the same.
func (t *backupTarget) appSecret(app string) string {
	h := sha256.Sum256([]byte(t.GatewayKey + ":" + app))
	return hex.EncodeToString(h[:])
}

const resticRepository = "s3:" + gatewayURL + "/volumes"

// backupResources are the per-app credentials and, for apps with a
// database, the barman object store.
func backupResources(app *cellv1.App, t *backupTarget) []client.Object {
	objs := []client.Object{
		&corev1.Secret{
			ObjectMeta: meta(app, backupSecret),
			StringData: map[string]string{
				// barman-cloud and restic name the same things differently
				"ACCESS_KEY_ID": app.Name, "ACCESS_SECRET_KEY": t.appSecret(app.Name), "REGION": gatewayRegion,
				"AWS_ACCESS_KEY_ID": app.Name, "AWS_SECRET_ACCESS_KEY": t.appSecret(app.Name), "AWS_DEFAULT_REGION": gatewayRegion,
				"RESTIC_PASSWORD":     t.Password,
				"CRYPT_ACCESS_KEY_ID": app.Name + ".db", "CRYPT_ACCESS_SECRET_KEY": t.appSecret(app.Name + ".db"),
			},
		},
	}
	if app.Spec.Needs.Database == nil {
		return objs
	}
	store := func(name, prefix string) client.Object {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "barmancloud.cnpg.io/v1", "kind": "ObjectStore",
			"metadata": map[string]any{"name": name, "namespace": Namespace(app.Name), "labels": toAny(labels(app))},
			"spec": map[string]any{
				"retentionPolicy": "30d",
				// The plugin's catalog check also records the last base
				// backup; backupArchive waits for it (archiveConfirm).
				"instanceSidecarConfiguration": map[string]any{"retentionPolicyIntervalSeconds": int64(catalogCheck / time.Second)},
				"configuration": map[string]any{
					"destinationPath": "s3://postgres",
					"endpointURL":     gatewayURL,
					"s3Credentials": map[string]any{
						"accessKeyId":     map[string]any{"name": backupSecret, "key": prefix + "ACCESS_KEY_ID"},
						"secretAccessKey": map[string]any{"name": backupSecret, "key": prefix + "ACCESS_SECRET_KEY"},
						"region":          map[string]any{"name": backupSecret, "key": "REGION"},
					},
					"wal":  map[string]any{"compression": "gzip"},
					"data": map[string]any{"compression": "gzip"},
				},
			},
		}}
	}
	// The plain store stays for the archives from before E-67.
	return append(objs, store(objectStore, ""), store(cryptStore, "CRYPT_"))
}

// newServerName names a fresh WAL archive, always an encrypted one. Every
// database cluster gets its own: the archive of a restored cluster must not
// mix with its origin's.
func newServerName(now time.Time) string {
	return cryptArchive + now.UTC().Format("20060102t150405")
}

// archiveStore is the ObjectStore an archive lives in.
func archiveStore(serverName string) string {
	if strings.HasPrefix(serverName, cryptArchive) {
		return cryptStore
	}
	return objectStore
}

func archiverPlugin(serverName string) map[string]any {
	return map[string]any{
		"name": barmanPlugin, "isWALArchiver": true,
		"parameters": map[string]any{"barmanObjectName": archiveStore(serverName), "serverName": serverName},
	}
}

// serverName reads the WAL archive name of a database cluster ("" if none).
func serverName(cluster *unstructured.Unstructured) string {
	plugins, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	for _, p := range plugins {
		m, _ := p.(map[string]any)
		if m["name"] == barmanPlugin {
			if sn, _, _ := unstructured.NestedString(m, "parameters", "serverName"); sn != "" {
				return sn
			}
		}
	}
	return ""
}

// jobName keeps generated Job names within the 63 character limit.
func jobName(prefix, name string) string {
	n := prefix + "-" + name
	if len(n) <= 63 {
		return n
	}
	h := sha256.Sum256([]byte(n))
	return strings.TrimRight(n[:54], "-.") + "-" + hex.EncodeToString(h[:])[:8]
}

// resticPrelude runs every restic of a script in the background, so that a
// stop request (SIGTERM to the shell, which as PID 1 would not pass it on)
// reaches restic: it then removes its repository lock instead of being
// killed with it held.
const resticPrelude = `set -eu
child=
trap '[ -z "$child" ] || { kill -TERM "$child" 2>/dev/null; wait "$child" || :; }; exit 143' TERM
restic() { command restic "$@" & child=$!; wait "$child"; }
`

// resticJob runs a restic script in the app namespace with the given
// volumes mounted at /data/<claim>. Its result is the container's
// termination message (stdout redirected there by the script).
func resticJob(app *cellv1.App, t *backupTarget, image, name, script string, claims []string, readOnly bool, env ...corev1.EnvVar) *batchv1.Job {
	secret := func(key string) corev1.EnvVar {
		return corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: backupSecret}, Key: key}}}
	}
	container := corev1.Container{
		Name:    "restic",
		Image:   image,
		Command: []string{"sh", "-c", resticPrelude + script},
		Env: append([]corev1.EnvVar{
			{Name: "RESTIC_REPOSITORY", Value: resticRepository},
			{Name: "RESTIC_CACHE_DIR", Value: "/tmp/restic-cache"},
			{Name: "APP", Value: app.Name},
			secret("AWS_ACCESS_KEY_ID"), secret("AWS_SECRET_ACCESS_KEY"), secret("AWS_DEFAULT_REGION"), secret("RESTIC_PASSWORD"),
		}, env...),
		VolumeMounts:             []corev1.VolumeMount{{Name: "cell", MountPath: resticDataRoot + "/.cell"}},
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
	volumes := []corev1.Volume{{Name: "cell", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	for i, c := range claims {
		v := fmt.Sprintf("v%d", i)
		volumes = append(volumes, corev1.Volume{Name: v, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: c, ReadOnly: readOnly}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: v, MountPath: resticDataRoot + "/" + c, ReadOnly: readOnly})
	}
	l := labels(app)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(app.Name), Labels: l},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr(int32(1)),
			TTLSecondsAfterFinished: ptr(int32(7 * 24 * 3600)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: l},
				Spec:       corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{container}, Volumes: volumes},
			},
		},
	}
}

// Scripts. They print their result as JSON into the termination message.
const (
	backupScript = `restic cat config >/dev/null 2>&1 || restic init
printf '{"app":"%s","backup":"%s"}' "$APP" "$BACKUP" > /data/.cell/backup.json
claims=""
for c in $CLAIMS; do claims="$claims --tag claim=$c"; done
restic backup --host "$APP" --tag "backup=$BACKUP" --tag "db-server=$DB_SERVER" --tag "db-backup=$DB_BACKUP" --tag claims=recorded $claims /data
restic forget --host "$APP" --keep-within 30d --prune
restic snapshots --json --host "$APP" --tag "backup=$BACKUP" --latest 1 > /dev/termination-log`
	lookupScript = `if [ "$BACKUP" = latest ]; then
  restic snapshots --json --host "$APP" --latest 1 > /dev/termination-log
else
  restic snapshots --json --host "$APP" --tag "backup=$BACKUP" --latest 1 > /dev/termination-log
fi`
	restoreScript = `for claim in $CLAIMS; do
  restic restore "$SNAPSHOT:/data/$claim" --target "/data/$claim" --delete
done
echo '[]' > /dev/termination-log`
)

// snapshot is what restic prints for a snapshot (the fields we use).
type snapshot struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
}

// tags returns every value of a repeated tag.
func (s snapshot) tags(key string) []string {
	var out []string
	for _, t := range s.Tags {
		if k, v, ok := strings.Cut(t, "="); ok && k == key {
			out = append(out, v)
		}
	}
	return out
}

func (s snapshot) tag(key string) string {
	for _, t := range s.Tags {
		if k, v, ok := strings.Cut(t, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// jobResult reports the outcome of a Job as ensureChild read it: done, its
// termination message, and an error when it failed (with the message too).
func jobResult(ctx context.Context, c client.Client, job *batchv1.Job) (done bool, message string, err error) {
	finished := ""
	for _, cond := range job.Status.Conditions {
		if cond.Status == corev1.ConditionTrue && (cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed) {
			finished = string(cond.Type)
		}
	}
	if finished == "" {
		return false, "", nil
	}
	var pods corev1.PodList
	// By the Job's UID: pods of an earlier Job with the same name may linger.
	if err := c.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{"batch.kubernetes.io/controller-uid": string(job.UID)}); err != nil {
		return false, "", err
	}
	for _, p := range pods.Items {
		for _, st := range p.Status.ContainerStatuses {
			if t := st.State.Terminated; t != nil {
				if finished == string(batchv1.JobComplete) && t.ExitCode == 0 {
					return true, t.Message, nil
				}
				message = t.Message
			}
		}
	}
	if finished == string(batchv1.JobComplete) {
		return false, "", fmt.Errorf("job %s completed but its pod is gone", job.Name)
	}
	return true, message, fmt.Errorf("job %s failed: %s", job.Name, strings.TrimSpace(message))
}

// parseSnapshot reads the single snapshot a script printed.
func parseSnapshot(message string) (snapshot, error) {
	var snaps []snapshot
	if err := json.Unmarshal([]byte(message), &snaps); err != nil {
		return snapshot{}, fmt.Errorf("restic output: %w", err)
	}
	if len(snaps) != 1 {
		return snapshot{}, fmt.Errorf("found %d matching snapshots", len(snaps))
	}
	return snaps[0], nil
}

// dataClaims lists the volumes of an app that restic covers (databases are
// backed up by barman).
func dataClaims(ctx context.Context, c client.Client, app *cellv1.App) ([]string, error) {
	var pvcs corev1.PersistentVolumeClaimList
	if err := c.List(ctx, &pvcs, client.InNamespace(Namespace(app.Name))); err != nil {
		return nil, err
	}
	var claims []string
	for _, p := range pvcs.Items {
		if _, isDB := p.Labels["cnpg.io/cluster"]; !isDB {
			claims = append(claims, p.Name)
		}
	}
	return claims, nil
}

// claimNode is the node of a running pod with one of claims mounted, or ""
// when none runs. A pod that mounts such a claim too must run there: a
// ReadWriteOnce volume is attached to one node at a time. Only a running pod
// has its volumes attached; one waiting for them (a backup on the wrong
// node) does not count.
// ponytail: one node for all claims; an app whose volumes are attached on
// different nodes needs a Job per node.
func claimNode(ctx context.Context, c client.Client, ns string, claims []string) (string, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(ns)); err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && slices.Contains(claims, v.PersistentVolumeClaim.ClaimName) {
				return p.Spec.NodeName, nil
			}
		}
	}
	return "", nil
}

// onNode keeps a pod on the named node, if any.
func onNode(spec *corev1.PodSpec, node string) {
	if node == "" {
		return
	}
	spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}},
		}}},
	}}
}

// pinnedNode is the node onNode keeps a pod on, or "".
func pinnedNode(spec *corev1.PodSpec) string {
	if a := spec.Affinity; a != nil && a.NodeAffinity != nil && a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		for _, t := range a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			for _, f := range t.MatchFields {
				if f.Key == "metadata.name" && f.Operator == corev1.NodeSelectorOpIn && len(f.Values) == 1 {
					return f.Values[0]
				}
			}
		}
	}
	return ""
}

// staleJob deletes a Job that is not kept on node, where its volumes are
// attached now (the app moved, or the Job is from before it was kept
// there), while none of its pods has started: it would wait for the
// volumes for good and hold its locks. A pod that has not started has not
// touched the backup. It reports whether the Job is deleted or still being
// deleted; the caller creates it again once it is gone. In the foreground:
// the Job is gone only after its pods, so a pod that started after the check
// never runs next to the new one.
func (r *BackupReconciler) staleJob(ctx context.Context, job *batchv1.Job, node string) (bool, error) {
	if job.DeletionTimestamp != nil {
		return true, nil
	}
	if node == "" || pinnedNode(&job.Spec.Template.Spec) == node {
		return false, nil
	}
	var pods corev1.PodList
	// Past the cache: a pod that has just started must not look pending.
	if err := apiReader(r.Reader, r).List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{"batch.kubernetes.io/controller-uid": string(job.UID)}); err != nil {
		return false, err
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodPending {
			return false, nil
		}
	}
	uid := job.UID
	err := r.Delete(ctx, job, client.Preconditions{UID: &uid}, client.PropagationPolicy(metav1.DeletePropagationForeground))
	return true, client.IgnoreNotFound(err)
}

// createOnce creates an object unless it exists.
func createOnce(ctx context.Context, c client.Client, o client.Object) error {
	if err := c.Create(ctx, o); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// labelRun binds a Job or database backup to the run (Backup or Restore)
// that created it, by UID: names repeat when a run is deleted and created
// again, results of the earlier run must not count for the new one.
var labelRun = cellv1.GroupVersion.Group + "/run-uid"

// ensureChild creates o for the run with the given UID and fills o with the
// child as it is now. It reports true only for a child of this run, so the
// caller never reads the status of an earlier run's child of the same name
// (also not from a cache that has not seen the replacement yet). A leftover
// of an earlier run is deleted; ensureChild then reports false and the
// caller tries again later.
func ensureChild(ctx context.Context, c client.Client, o client.Object, uid string) (bool, error) {
	l := o.GetLabels()
	if l == nil {
		l = map[string]string{}
	}
	l[labelRun] = uid
	o.SetLabels(l)
	if err := c.Create(ctx, o); err == nil || !apierrors.IsAlreadyExists(err) {
		return err == nil, err
	}
	existing := o.DeepCopyObject().(client.Object)
	existing.SetLabels(nil)
	if err := c.Get(ctx, client.ObjectKeyFromObject(o), existing); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if existing.GetLabels()[labelRun] == uid {
		reflect.ValueOf(o).Elem().Set(reflect.ValueOf(existing).Elem())
		return true, nil
	}
	if existing.GetDeletionTimestamp() == nil {
		// Only the object that was read: a stale read must not delete the
		// replacement.
		rv := existing.GetResourceVersion()
		err := c.Delete(ctx, existing, client.PropagationPolicy(metav1.DeletePropagationBackground),
			client.Preconditions{ResourceVersion: &rv})
		if client.IgnoreNotFound(err) != nil && !apierrors.IsConflict(err) {
			return false, err
		}
	}
	return false, nil
}
