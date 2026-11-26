package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// The backup gateway's auth proxy derives the same key in sh:
// printf '%s:%s' "$key" "$app" | sha256sum
func TestAppSecret(t *testing.T) {
	if got := (&backupTarget{GatewayKey: "gk"}).appSecret("notes"); got != "efe2d6c2964358676bd8fb3ca69af3256d04aa3fdfb761030246929a2844d53b" {
		t.Errorf("app secret %s", got)
	}
}

// The gateway's auth proxy gives an app's key its plain directory and the
// "<app>.db" key the encrypted database archive, keyed by the backup
// password alone (E-67), and refuses any other key id.
func TestGatewayProxy(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/base/backup-gateway.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := yaml.Unmarshal([]byte(strings.SplitN(string(raw), "\n---\n", 2)[0]), &cm); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{"gatewayKey": "gk", "root": "bucket/cell", "password": "pw"}
	for k, v := range files {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.ReplaceAll(cm.Data["proxy"], "/target/", dir+"/")
	proxy := func(user string) (map[string]string, error) {
		cmd := exec.Command("sh", "-c", script)
		cmd.Stdin = strings.NewReader(`{"user": "` + user + `", "pass": "x"}`)
		out, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		var m map[string]string
		return m, json.Unmarshal(out, &m)
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	gk := &backupTarget{GatewayKey: "gk"}

	plain, err := proxy("notes")
	if err != nil || plain["type"] != "alias" || plain["remote"] != "target:bucket/cell/apps/notes" ||
		plain["_secret_access_key"] != gk.appSecret("notes") {
		t.Errorf("app key: %v %v", plain, err)
	}
	db, err := proxy("notes.db")
	if err != nil || db["type"] != "crypt" || db["remote"] != "target:bucket/cell/apps/notes/crypt" ||
		db["_secret_access_key"] != gk.appSecret("notes.db") || db["_obscure"] != "password,password2" ||
		db["password"] != sum("crypt:notes:pw") || db["password2"] != sum("crypt-salt:notes:pw") {
		t.Errorf("database key: %v %v", db, err)
	}
	for _, bad := range []string{"", ".db", "notes.db.db", "Notes", "-notes", "notes-.db", "a/b"} {
		if out, err := proxy(bad); err == nil {
			t.Errorf("key id %q accepted: %v", bad, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "password"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := proxy("notes.db"); err == nil {
		t.Errorf("archive without backup password: %v", out)
	}
	if out, err := proxy("notes"); err != nil {
		t.Errorf("app key needs no backup password: %v %v", out, err)
	}
}

// New archives are encrypted; an archive from before E-67 is restored from
// the plain store it lives in.
func TestArchiveStores(t *testing.T) {
	app := &cellv1.App{ObjectMeta: metav1.ObjectMeta{Name: "notes"},
		Spec: cellv1.AppSpec{Needs: cellv1.Needs{Database: &cellv1.Database{Engine: "postgres"}}}}
	sn := newServerName(time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC))
	if sn != "crypt-20261126t030000" || archiverPlugin(sn)["parameters"].(map[string]any)["barmanObjectName"] != cryptStore {
		t.Errorf("new archive %s: %v", sn, archiverPlugin(sn))
	}
	for from, want := range map[string]string{"db-20261101t000000": objectStore, sn: cryptStore} {
		u := recoveryDatabase(app, cellv1.DatabaseBackup{ServerName: from, BackupID: "b"}, sn, "uid")
		ext, _, _ := unstructured.NestedSlice(u.Object, "spec", "externalClusters")
		if got, _, _ := unstructured.NestedString(ext[0].(map[string]any), "plugin", "parameters", "barmanObjectName"); got != want {
			t.Errorf("restore of %s reads %s, want %s", from, got, want)
		}
	}
	objs := backupResources(app, &backupTarget{Password: "pw", GatewayKey: "gk"})
	creds := objs[0].(*corev1.Secret).StringData
	if creds["CRYPT_ACCESS_KEY_ID"] != "notes.db" || creds["CRYPT_ACCESS_SECRET_KEY"] != (&backupTarget{GatewayKey: "gk"}).appSecret("notes.db") {
		t.Errorf("credentials %v", creds)
	}
	if len(objs) != 3 || objs[1].GetName() != objectStore || objs[2].GetName() != cryptStore {
		t.Fatalf("stores %v", objs)
	}
	key, _, _ := unstructured.NestedString(objs[2].(*unstructured.Unstructured).Object, "spec", "configuration", "s3Credentials", "accessKeyId", "key")
	if key != "CRYPT_ACCESS_KEY_ID" {
		t.Errorf("encrypted store signs with %s", key)
	}
}
