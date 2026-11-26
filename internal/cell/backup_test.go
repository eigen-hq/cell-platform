package cell

import (
	"os"
	"strings"
	"testing"
)

// Plain http only to a private address, asked for on purpose; the
// installer runs the same cases (deploy/lib_test.sh).
func TestCheckBackupEndpoint(t *testing.T) {
	raw, err := os.ReadFile("testdata/endpoints.txt")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		n++
		err := CheckBackupEndpoint(f[0], f[1] == "true")
		if got := map[bool]string{true: "ok", false: "refused"}[err == nil]; got != f[2] {
			t.Errorf("%s private=%s: %s (%v), want %s", f[0], f[1], got, err, f[2])
		}
	}
	if n < 20 {
		t.Fatalf("only %d cases read", n)
	}
	// The endpoint goes into rclone.conf: a line break must not pass.
	if CheckBackupEndpoint("https://s3.example.com\nendpoint = http://203.0.113.5", false) == nil {
		t.Error("an endpoint with a line break passed")
	}
}

// What the cell tells about each kind of target, as bootstrap or the
// operator wrote it, and never a secret.
func TestDescribeBackupTarget(t *testing.T) {
	conf, err := S3RcloneConf("https://fsn1.your-objectstorage.com", "AKID", "s3cr3t/key+", "fsn1")
	if err != nil {
		t.Fatal(err)
	}
	data := func(kv ...string) map[string][]byte {
		m := map[string][]byte{"password": []byte("pw"), "gatewayKey": []byte("gk")}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = []byte(kv[i+1])
		}
		return m
	}
	s3 := DescribeBackupTarget(data("rclone.conf", conf, "root", "bucket/cell.example", "prefix", "cell.example"))
	want := BackupTarget{Configured: true, Kind: "s3", Where: "https://fsn1.your-objectstorage.com/bucket/cell.example",
		Endpoint: "https://fsn1.your-objectstorage.com", Region: "fsn1", Bucket: "bucket", Prefix: "cell.example", AccessKey: "AKID"}
	if s3 != want {
		t.Errorf("s3: %+v", s3)
	}
	if none := DescribeBackupTarget(data("rclone.conf", "", "root", "", "prefix", "cell.example")); none != (BackupTarget{Prefix: "cell.example"}) {
		t.Errorf("none: %+v", none)
	}
	local := DescribeBackupTarget(data("rclone.conf", "[target]\ntype = local\n", "root", "/backup/cell.example",
		"localVolume", `hostPath: {path: "/mnt/nas", type: DirectoryOrCreate}`))
	if local.Kind != "local" || local.Where != "/mnt/nas/cell.example on the first machine" {
		t.Errorf("local: %+v", local)
	}
	other := DescribeBackupTarget(data("rclone.conf", "[other]\ntype = s3\n# type = s3\n[target]\ntype = sftp\nhost = nas\npass = x\n", "root", "backups/cell.example"))
	if other.Kind != "other" || other.Where != "sftp remote, backups/cell.example" || other.Endpoint != "" {
		t.Errorf("other: %+v", other)
	}

	// A value that would add an option of its own is refused.
	// So is one rclone's parser would read as more than itself.
	for _, bad := range []string{"key\nendpoint = https://evil", "key with space", "", `"""k"""`, "`k`", "%(access_key_id)s", "k;x", "k#x"} {
		if _, err := S3RcloneConf("https://s3.test", "AKID", bad, ""); err == nil {
			t.Errorf("secret key %q accepted", bad)
		}
	}
	if c, _ := S3RcloneConf("https://s3.test", "AKID", "k", ""); !strings.Contains(c, "\nregion = us-east-1\n") {
		t.Errorf("default region: %q", c)
	}
}
