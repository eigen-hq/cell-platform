package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// What viewers may read (E-63). A new route stays closed to them unless it
// is added here on purpose.
var viewerRoutes = []string{
	"GET /v0/catalog", "GET /v0/catalog/{name}", "GET /v0/apps", "GET /v0/apps/{name}",
	"GET /v0/apps/{name}/backups", "GET /v0/apps/{name}/access", "GET /v0/changes", "GET /v0/changes/{name}",
	"GET /v0/events", "GET /v0/nodes", "GET /v0/backup-target",
}

// Every read open to viewers, with the fixtures of TestViewerRedaction.
var viewerReads = []string{"/v0/apps", "/v0/apps/notes", "/v0/apps/notes/backups", "/v0/apps/notes/access",
	"/v0/changes", "/v0/changes/by-agent", "/v0/changes/by-human", "/v0/backup-target"}

func TestViewerRoutes(t *testing.T) {
	var got []string
	for _, rt := range (&Server{}).routes() {
		if rt.role == RoleViewer {
			got = append(got, rt.method+" "+rt.path)
		}
	}
	if !slices.Equal(got, viewerRoutes) {
		t.Errorf("routes for viewers:\n got %v\nwant %v", got, viewerRoutes)
	}
}

// Viewers see how an app is reached, not its configuration, and no patch
// or agent reason, which can quote logs.
func TestViewerRedaction(t *testing.T) {
	app := installed("notes", "oidc")
	app.Spec.Source.Image.Env = []corev1.EnvVar{{Name: "SMTP_PASSWORD", Value: "hunter2"}}
	app.Status.Conditions[0].Message = "rendering env SMTP_PASSWORD=hunter2"
	cr := func(name, kind, reason string) *cellv1.ChangeRequest {
		return &cellv1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: cellv1.ChangeRequestSpec{App: "notes", Risk: "low", Reason: reason, Requester: cellv1.Requester{Kind: kind, Name: "x"},
				Patch: &apiextensionsv1.JSON{Raw: []byte(`{"version":"2"}`)}},
			Status: cellv1.ChangeRequestStatus{Message: "failed: hunter2"}}
	}
	backup := &cellv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "notes-1"}, Spec: cellv1.BackupSpec{App: "notes"},
		Status: cellv1.BackupStatus{Phase: cellv1.RunFailed, Message: "dump: hunter2"}}
	status, _ := json.Marshal(cell.BackupTarget{Configured: true, Kind: "s3", Where: "https://s3.test/b", AccessKey: "AKID",
		Check: &cell.BackupCheck{State: cell.CheckFailed, Message: "AKID denied", Target: "https://s3.test/b"}})
	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: cell.BackupTargetStatus},
		Data: map[string]string{cell.BackupTargetStatusKey: string(status)}}
	h := newHarness(t, app, cr("by-agent", "Agent", "the log says hunter2"), cr("by-human", "Human", "planned update"), backup, target)

	// No sentinel anywhere a viewer reads; an admin still gets all of it.
	for _, path := range viewerReads {
		if code, body := h.do("GET", path, "viewer", nil); code != 200 || strings.Contains(fmt.Sprint(body), "hunter2") || strings.Contains(fmt.Sprint(body), "AKID") {
			t.Errorf("viewer %s: %d %v", path, code, body)
		}
	}
	for _, path := range []string{"/v0/apps/notes", "/v0/changes/by-agent", "/v0/apps/notes/backups", "/v0/backup-target"} {
		if _, body := h.do("GET", path, "admin", nil); !strings.Contains(fmt.Sprint(body), "hunter2") && !strings.Contains(fmt.Sprint(body), "AKID") {
			t.Errorf("admin %s lost its detail: %v", path, body)
		}
	}

	_, body := h.do("GET", "/v0/apps/notes", "viewer", nil)
	if spec, _ := json.Marshal(field(body, "spec")); string(spec) != `{"access":{"auth":{"mode":"oidc","secretKeys":{}},"port":80,"subdomain":"notes"}}` {
		t.Errorf("viewer spec %s", spec)
	}
	_, body = h.do("GET", "/v0/apps", "viewer", nil)
	if field(body.([]any)[0], "spec", "source") != nil {
		t.Errorf("viewer list spec %v", field(body.([]any)[0], "spec"))
	}
	if _, body = h.do("GET", "/v0/apps/notes", "admin", nil); field(body, "spec", "source") == nil {
		t.Errorf("admin spec %v", field(body, "spec"))
	}

	_, body = h.do("GET", "/v0/changes/by-agent", "viewer", nil)
	if field(body, "patch") != nil || field(body, "reason") != "" {
		t.Errorf("viewer sees agent change %v", body)
	}
	_, body = h.do("GET", "/v0/changes", "viewer", nil)
	for _, c := range body.([]any) {
		if field(c, "patch") != nil || (field(c, "name") == "by-human") != (field(c, "reason") != "") {
			t.Errorf("viewer change list %v", c)
		}
	}
	if _, body = h.do("GET", "/v0/changes/by-agent", "admin", nil); field(body, "patch") == nil || field(body, "reason") == "" {
		t.Errorf("admin change %v", body)
	}

	for _, path := range []string{"/v0/users", "/v0/groups", "/v0/tasks", "/v0/apps/notes/pods", "/v0/apps/notes/events", "/v0/apps/notes/logs"} {
		if code, _ := h.do("GET", path, "viewer", nil); code != 403 {
			t.Errorf("viewer reads %s: %d", path, code)
		}
	}
}
