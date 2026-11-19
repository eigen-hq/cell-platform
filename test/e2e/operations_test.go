//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/marcelheidebrecht/cell-platform/api/v1alpha1"
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

func (m marker) onVolume(script string) string {
	overrides, _ := json.Marshal(map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{
			"name": "marker", "image": "busybox:1.37", "command": []any{"sh", "-c", script},
			"volumeMounts": []any{map[string]any{"name": "v", "mountPath": "/v"}},
		}},
		"volumes": []any{map[string]any{"name": "v", "persistentVolumeClaim": map[string]any{"claimName": m.claim()}}},
	}})
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

func waitRun[T client.Object](t *testing.T, obj T, timeout time.Duration, phase func(T) (cellv1.RunPhase, string), want cellv1.RunPhase) {
	t.Helper()
	waitFor(t, timeout, func() error {
		if err := kube(t).Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		p, msg := phase(obj)
		if p == want {
			return nil
		}
		if p == cellv1.RunFailed || (p != "" && want == cellv1.RunCompleted && p != cellv1.RunRunning && p != cellv1.RunPending) {
			t.Fatalf("%s ended %s: %s", obj.GetName(), p, msg)
		}
		return fmt.Errorf("%s is %s: %s", obj.GetName(), p, msg)
	})
}

func backupPhase(b *cellv1.Backup) (cellv1.RunPhase, string)   { return b.Status.Phase, b.Status.Message }
func restorePhase(r *cellv1.Restore) (cellv1.RunPhase, string) { return r.Status.Phase, r.Status.Message }
func changePhase(c *cellv1.ChangeRequest) (cellv1.RunPhase, string) {
	return c.Status.Phase, c.Status.Message
}

func backupNow(t *testing.T, app string) *cellv1.Backup {
	t.Helper()
	b := &cellv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-e2e-%d", app, time.Now().Unix())},
		Spec:       cellv1.BackupSpec{App: app, Reason: "manual"},
	}
	if err := kube(t).Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	waitRun(t, b, 10*time.Minute, backupPhase, cellv1.RunCompleted)
	return b
}

func restoreNow(t *testing.T, app, backup string) {
	t.Helper()
	rs := &cellv1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-e2e-%d", app, time.Now().Unix())},
		Spec:       cellv1.RestoreSpec{App: app, Backup: backup},
	}
	if err := kube(t).Create(context.Background(), rs); err != nil {
		t.Fatal(err)
	}
	waitRun(t, rs, 15*time.Minute, restorePhase, cellv1.RunCompleted)
}

// M2: back up, change data, restore: database and volume are as backed up.
func TestBackupAndRestore(t *testing.T) {
	tc := newTestCell(t)
	for _, name := range []string{"forgejo", "nextcloud"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			install(t, tc, loadManifest(t, name), 15*time.Minute)
			m := marker{t: t, app: name}
			m.write("before-backup")
			b := backupNow(t, name)
			m.write("after-backup")
			restoreNow(t, name, b.Name)
			install(t, tc, loadManifest(t, name), 15*time.Minute)
			m.expect("before-backup")
		})
	}
}

// M2: an update that does not become healthy is rolled back automatically:
// version and data are as before, and the change log says what happened.
func TestFailedUpdateRollsBack(t *testing.T) {
	tc := newTestCell(t)
	ctx := context.Background()
	install(t, tc, loadManifest(t, "forgejo"), 15*time.Minute)
	m := marker{t: t, app: "forgejo"}
	m.write("before-update")

	cr := &cellv1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("forgejo-broken-update-%d", time.Now().Unix())},
		Spec: cellv1.ChangeRequestSpec{
			App:       "forgejo",
			Requester: cellv1.Requester{Kind: "Human", Name: "e2e"},
			Reason:    "update to a version that does not exist",
			Risk:      "medium",
			Patch:     &apiextensionsv1.JSON{Raw: []byte(`{"version":"15.0.999","health":{"timeoutSeconds":90}}`)},
			Approval:  cellv1.Approval{Required: true, Decision: "Approved", By: "e2e"},
		},
	}
	if err := tc.kube.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	waitRun(t, cr, 20*time.Minute, changePhase, cellv1.RunRolledBack)
	if cr.Status.Backup == "" || cr.Status.Restore == "" || cr.Status.PreviousSpec == nil {
		t.Errorf("change log incomplete: %+v", cr.Status)
	}
	t.Logf("change log: %s", cr.Status.Message)

	var app cellv1.App
	waitFor(t, 5*time.Minute, func() error {
		if err := tc.kube.Get(ctx, client.ObjectKey{Name: "forgejo"}, &app); err != nil {
			return err
		}
		if app.Spec.Version != "15.0.11" || app.Status.Version != "15.0.11" || app.Status.Phase != cellv1.PhaseNeedsAttention {
			return fmt.Errorf("app %s/%s %s", app.Spec.Version, app.Status.Version, app.Status.Phase)
		}
		return nil
	})
	install(t, tc, loadManifest(t, "forgejo"), 10*time.Minute)
	m.expect("before-update")
}
