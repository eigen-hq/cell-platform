//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// kubectl runs the pinned kubectl against the local cell.
func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(repoRoot(), "bin", "kubectl"), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s%s", args, err, out, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// marker writes a value into an app's database and into its first data
// volume, or reads both back. It works for every app the platform runs:
// the database is the app's CloudNativePG cluster, the volume is mounted
// by a short-lived pod next to the app.
type marker struct {
	t   *testing.T
	app string
}

func (m marker) ns() string { return "app-" + m.app }

func (m marker) db() string { return strings.ReplaceAll(m.app, "-", "_") }

func (m marker) sql(query string) string {
	return kubectl(m.t, "-n", m.ns(), "exec", "db-1", "-c", "postgres", "--",
		"psql", "-qtA", "-U", "postgres", "-d", m.db(), "-c", query)
}

func (m marker) claim() string {
	var pvcs corev1.PersistentVolumeClaimList
	if err := kube(m.t).List(context.Background(), &pvcs, client.InNamespace(m.ns())); err != nil {
		m.t.Fatal(err)
	}
	for _, p := range pvcs.Items {
		if _, isDB := p.Labels["cnpg.io/cluster"]; !isDB {
			return p.Name
		}
	}
	m.t.Fatalf("%s has no data volume", m.app)
	return ""
}

// node is where a running pod of the app has claim attached, or "".
func (m marker) node(claim string) string {
	var pods corev1.PodList
	if err := kube(m.t).List(context.Background(), &pods, client.InNamespace(m.ns())); err != nil {
		m.t.Fatal(err)
	}
	for _, p := range pods.Items {
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == claim && p.Status.Phase == corev1.PodRunning {
				return p.Spec.NodeName
			}
		}
	}
	return ""
}

func (m marker) onVolume(script string) string {
	claim := m.claim()
	spec := map[string]any{
		"containers": []any{map[string]any{
			"name": "marker", "image": "busybox:1.37", "command": []any{"sh", "-c", script},
			"volumeMounts": []any{map[string]any{"name": "v", "mountPath": "/v"}},
		}},
		"volumes": []any{map[string]any{"name": "v", "persistentVolumeClaim": map[string]any{"claimName": claim}}},
	}
	// A ReadWriteOnce volume is attached to one node at a time.
	if node := m.node(claim); node != "" {
		spec["nodeName"] = node
	}
	overrides, _ := json.Marshal(map[string]any{"spec": spec})
	pod := fmt.Sprintf("marker-%d", time.Now().UnixNano())
	kubectl(m.t, "-n", m.ns(), "run", pod, "--restart=Never", "--image=busybox:1.37", "--overrides="+string(overrides))
	defer kubectl(m.t, "-n", m.ns(), "delete", "pod", pod, "--wait=false")
	kubectl(m.t, "-n", m.ns(), "wait", "pod/"+pod, "--for=jsonpath={.status.phase}=Succeeded", "--timeout=3m")
	return kubectl(m.t, "-n", m.ns(), "logs", pod)
}

func (m marker) write(value string) {
	m.sql("create table if not exists cell_marker (v text); delete from cell_marker; insert into cell_marker values ('" + value + "')")
	m.onVolume("echo " + value + " > /v/.cell-marker")
}

func (m marker) read() (db, file string) {
	// A fresh installation has no marker table yet.
	return m.sql("create table if not exists cell_marker (v text); select v from cell_marker"),
		m.onVolume("cat /v/.cell-marker 2>/dev/null || true")
}

func (m marker) expect(value string) {
	m.t.Helper()
	if db, file := m.read(); db != value || file != value {
		m.t.Fatalf("%s: database has %q, volume has %q, want %q", m.app, db, file, value)
	}
}

// Through the API: back up, change data, restore: database and
// volume are as backed up.
func TestBackupAndRestore(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	for _, name := range []string{"forgejo", "nextcloud"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			admin.installed(t, name, 15*time.Minute)
			m := marker{t: t, app: name}
			m.write("before-backup")
			b := admin.backup(t, name)
			m.write("after-backup")
			admin.restore(t, name, b)
			m.expect("before-backup")
		})
	}
}

// Through the API: an update that does not become healthy is rolled
// back automatically: version and data are as before, and the change log
// says what happened.
func TestFailedUpdateRollsBack(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	admin.installed(t, "forgejo", 15*time.Minute)
	m := marker{t: t, app: "forgejo"}
	m.write("before-update")
	t.Log("forgejo: marker written to database and volume, update to 15.0.999 (does not exist) approved")

	change := admin.propose(t, admin, http.MethodPatch, "/v0/apps/forgejo", map[string]any{
		"patch":  map[string]any{"version": "15.0.999", "health": map[string]any{"timeoutSeconds": 90}},
		"reason": "update to a version that does not exist",
	})
	cr := admin.waitChange(t, change, 20*time.Minute, "RolledBack")
	if field(cr, "backup") == nil || field(cr, "restore") == nil {
		t.Errorf("change log incomplete: %v", cr)
	}
	t.Logf("change log: %s", field(cr, "message"))

	waitFor(t, 5*time.Minute, func() error {
		app := admin.do(t, http.MethodGet, "/v0/apps/forgejo", nil, http.StatusOK)
		if field(app, "version") != "15.0.11" || field(app, "runningVersion") != "15.0.11" || field(app, "phase") != "NeedsAttention" {
			return fmt.Errorf("app %v/%v %v", field(app, "version"), field(app, "runningVersion"), field(app, "phase"))
		}
		return nil
	})
	admin.installed(t, "forgejo", 10*time.Minute)
	m.expect("before-update")
	t.Log("forgejo runs 15.0.11 again, with the marker from before the update")
}

// Through the API: a restart replaces the pods of an image app and of a
// chart app, with spec and data unchanged. On the chart app, Flux checks
// for drift afterwards and keeps the restart's mark: the template does not
// change again.
func TestRestart(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	for _, name := range []string{"forgejo", "nextcloud"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			admin.installed(t, name, 15*time.Minute)
			ns := "app-" + name
			before := appPods(t, ns)
			change := admin.propose(t, admin, http.MethodPost, "/v0/apps/"+name+"/restart", map[string]any{"reason": "e2e"})
			cr := admin.waitChange(t, change, 10*time.Minute, "Succeeded")
			if field(cr, "backup") != "none" || field(cr, "restore") != nil {
				t.Errorf("restart backed up or restored: %v", cr)
			}
			waitFor(t, 5*time.Minute, func() error {
				for uid := range appPods(t, ns) {
					if before[uid] {
						return fmt.Errorf("pod %s from before the restart still runs", uid)
					}
				}
				return nil
			})
			marks := restartMarks(t, ns)
			if len(marks) == 0 {
				t.Fatalf("%s: no workload carries the restart's mark", name)
			}
			if name == "forgejo" {
				at := time.Now().UTC().Format(time.RFC3339Nano)
				kubectl(t, "-n", ns, "annotate", "helmrelease", name, "--overwrite", "reconcile.fluxcd.io/requestedAt="+at)
				kubectl(t, "-n", ns, "wait", "helmrelease/"+name, "--for=jsonpath={.status.lastHandledReconcileAt}="+at, "--timeout=5m")
				if after := restartMarks(t, ns); fmt.Sprint(after) != fmt.Sprint(marks) {
					t.Errorf("drift detection changed the restarted workloads: %v, before %v", after, marks)
				}
			}
			admin.installed(t, name, 5*time.Minute)
			t.Logf("%s: %d pods replaced, change log: %s", name, len(before), field(cr, "message"))
		})
	}
}

// appPods are the running pods of an app's workloads, without its database.
func appPods(t *testing.T, ns string) map[string]bool {
	t.Helper()
	var pods corev1.PodList
	if err := kube(t).List(context.Background(), &pods, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	uids := map[string]bool{}
	for _, p := range pods.Items {
		owner := metav1.GetControllerOf(&p)
		if _, isDB := p.Labels["cnpg.io/cluster"]; isDB || owner == nil || (owner.Kind != "ReplicaSet" && owner.Kind != "StatefulSet") || p.DeletionTimestamp != nil {
			continue
		}
		uids[string(p.UID)] = true
	}
	return uids
}

// restartMarks are the restart annotation and generation of each workload.
func restartMarks(t *testing.T, ns string) map[string]string {
	t.Helper()
	ctx := context.Background()
	var deploys appsv1.DeploymentList
	var sets appsv1.StatefulSetList
	if err := kube(t).List(ctx, &deploys, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if err := kube(t).List(ctx, &sets, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	marks := map[string]string{}
	for _, d := range deploys.Items {
		if at := d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; at != "" {
			marks["deployment/"+d.Name] = fmt.Sprintf("%s@%d", at, d.Generation)
		}
	}
	for _, s := range sets.Items {
		if at := s.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; at != "" {
			marks["statefulset/"+s.Name] = fmt.Sprintf("%s@%d", at, s.Generation)
		}
	}
	return marks
}
