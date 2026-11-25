package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// The API tells what the operator wrote about the target, and an admin's
// new target becomes a request for the operator to check, one at a time.
func TestBackupTarget(t *testing.T) {
	h := newHarness(t)
	code, body := h.do("GET", "/v0/backup-target", "viewer", nil)
	if code != 200 || field(body, "configured") != false {
		t.Fatalf("before the operator wrote anything: %d %v", code, body)
	}

	status, _ := json.Marshal(cell.BackupTarget{Configured: true, Kind: "s3", Where: "https://s3.test/b/cell", Endpoint: "https://s3.test",
		Region: "us-east-1", Bucket: "b", Prefix: "cell", AccessKey: "AKID"})
	if err := h.kube.Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: cell.BackupTargetStatus},
		Data:       map[string]string{cell.BackupTargetStatusKey: string(status)},
	}); err != nil {
		t.Fatal(err)
	}
	if code, body := h.do("GET", "/v0/backup-target", "viewer", nil); code != 200 || field(body, "where") != "https://s3.test/b/cell" || field(body, "check") != nil {
		t.Errorf("get: %d %v", code, body)
	}

	good := map[string]any{"endpoint": "https://fsn1.your-objectstorage.com/", "bucket": "cell-backups", "accessKey": "AK", "secretKey": "s3cr3t/+="}
	if code, _ := h.do("PUT", "/v0/backup-target", "viewer", good); code != 403 {
		t.Errorf("viewer sets the target: %d", code)
	}
	if code, _ := h.do("PUT", "/v0/backup-target", "agent", good); code != 403 {
		t.Errorf("agent sets the target: %d", code)
	}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for key, val := range good {
			m[key] = val
		}
		m[k] = v
		return m
	}
	for name, bad := range map[string]map[string]any{
		"ftp endpoint":    with("endpoint", "ftp://s3.test"),
		"endpoint path":   with("endpoint", "https://s3.test/bucket"),
		"endpoint query":  with("endpoint", "https://s3.test?x=1"),
		"user in url":     with("endpoint", "https://me@s3.test"),
		"bucket quote":    with("bucket", `b"x`),
		"bucket slash":    with("bucket", "b/x"),
		"newline in key":  with("secretKey", "k\nendpoint = https://evil"),
		"space in key":    with("accessKey", "a b"),
		"prefix dots":     with("prefix", "a/../b"),
		"prefix slash":    with("prefix", "/a"),
		"prefix quote":    with("prefix", `a"`),
		"region capitals": with("region", "EU"),
	} {
		if code, _ := h.do("PUT", "/v0/backup-target", "admin", bad); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}

	code, body = h.do("PUT", "/v0/backup-target", "admin", good)
	if code != 202 || field(body, "check", "state") != "checking" || field(body, "check", "by") != "admin-user" ||
		field(body, "check", "at") != "2026-11-19T12:00:00Z" || field(body, "check", "target") != "https://fsn1.your-objectstorage.com/cell-backups/cell" ||
		field(body, "where") != "https://s3.test/b/cell" {
		t.Errorf("put: %d %v", code, body)
	}
	if strings.Contains(strings.ToLower(jsonOf(body)), "s3cr3t") {
		t.Errorf("the answer holds the secret key: %v", body)
	}
	var req corev1.Secret
	if err := h.kube.Get(context.Background(), client.ObjectKey{Namespace: "cell-system", Name: cell.BackupTargetRequest}, &req); err != nil {
		t.Fatal(err)
	}
	d := req.StringData
	if d["endpoint"] != "https://fsn1.your-objectstorage.com" || d["region"] != "us-east-1" || d["bucket"] != "cell-backups" || d["prefix"] != "" ||
		d["secretKey"] != "s3cr3t/+=" || d["requester"] != "admin-user" || d["requestedAt"] != "2026-11-19T12:00:00Z" {
		t.Errorf("request: %v", d)
	}
	if code, _ := h.do("PUT", "/v0/backup-target", "admin", good); code != 409 {
		t.Errorf("second check while one runs: %d", code)
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
