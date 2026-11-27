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
// password alone (E-67), with its bucket always there, and refuses any
// other key id. It writes nothing: the key is checked after it ran.
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
	// rclone only obscures.
	fakeRclone := `#!/bin/sh
[ "$1" = obscure ] && echo "obscured-$2"
`
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "rclone"), []byte(fakeRclone), 0o700); err != nil {
		t.Fatal(err)
	}
	proxy := func(user string) (map[string]string, error) {
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
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
	upstream := func(root string) string {
		return `"postgres=:crypt,remote='target:` + root + `/apps/notes/crypt',password='obscured-` + sum("crypt:notes:pw") +
			"',password2='obscured-" + sum("crypt-salt:notes:pw") + `':postgres"`
	}
	if err != nil || db["type"] != "combine" || db["upstreams"] != upstream("bucket/cell") || db["_secret_access_key"] != gk.appSecret("notes.db") {
		t.Errorf("database key: %v %v", db, err)
	}
	// Any root a target may have: quoted for the list, the connection
	// string and JSON.
	if err := os.WriteFile(filepath.Join(dir, "root"), []byte(`my "bucket"/O'Reilly\x`), 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err = proxy("notes")
	if err != nil || plain["remote"] != `target:my "bucket"/O'Reilly\x/apps/notes` {
		t.Errorf("app key under a root with quotes: %v %v", plain, err)
	}
	if db, err := proxy("notes.db"); err != nil || db["upstreams"] != upstream(`my ""bucket""/O''Reilly\x`) {
		t.Errorf("database key under a root with quotes: %v %v", db, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root"), []byte(files["root"]), 0o600); err != nil {
		t.Fatal(err)
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
	// An unrecorded backup waits for one catalog check of the plugin.
	for _, o := range objs[1:] {
		every, _, _ := unstructured.NestedInt64(o.(*unstructured.Unstructured).Object, "spec", "instanceSidecarConfiguration", "retentionPolicyIntervalSeconds")
		if every <= 0 || time.Duration(every)*time.Second >= archiveConfirm {
			t.Errorf("store %s checks its catalog every %d s, the backup waits %s", o.GetName(), every, archiveConfirm)
		}
	}
}
