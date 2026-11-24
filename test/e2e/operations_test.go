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

	corev1 "k8s.io/api/core/v1"
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
