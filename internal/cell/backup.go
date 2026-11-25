package cell

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// The backup target (E-47). Bootstrap or the operator writes it into Secret
// BackupTargetSecret, which only the backup gateway mounts. An admin asks
// for a new one through the API (Secret BackupTargetRequest); the operator
// checks it and tells what the target is, without its secrets, in
// ConfigMap BackupTargetStatus (key BackupTargetStatusKey), which the API
// serves. The API never reads BackupTargetSecret.
const (
	BackupTargetSecret    = "backup-target"
	BackupTargetRequest   = "backup-target-request"
	BackupTargetStatus    = "backup-target-status"
	BackupTargetStatusKey = "target.json"

	CheckRunning = "checking"
	CheckPassed  = "passed"
	CheckFailed  = "failed"

	// DefaultRegion is what bootstrap sets when none is given; S3 clients
	// want one, most providers take any.
	DefaultRegion = "us-east-1"
)

// BackupTarget is what the cell tells about its backup target.
type BackupTarget struct {
	Configured bool   `json:"configured"`
	Kind       string `json:"kind,omitempty"`  // s3 | local | other
	Where      string `json:"where,omitempty"` // for people
	Endpoint   string `json:"endpoint,omitempty"`
	Region     string `json:"region,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
	// Prefix is the path in the target, also without one (the default for
	// the next).
	Prefix    string       `json:"prefix,omitempty"`
	AccessKey string       `json:"accessKey,omitempty"`
	Check     *BackupCheck `json:"check,omitempty"`
}

// BackupCheck is the last check of a target an admin asked for.
type BackupCheck struct {
	State   string `json:"state"` // CheckRunning | CheckPassed | CheckFailed
	Message string `json:"message,omitempty"`
	// Target is where the checked target points, also when it failed.
	Target string `json:"target"`
	// At: asked at while checking, ended at afterwards.
	At time.Time `json:"at"`
	By string    `json:"by"`
}

var hostPath = regexp.MustCompile(`path: "([^"]*)"`)

// DescribeBackupTarget tells what Secret BackupTargetSecret holds. A target
// is configured iff its rclone.conf is not empty. Secrets stay out.
func DescribeBackupTarget(data map[string][]byte) BackupTarget {
	raw := string(data["rclone.conf"])
	root := string(data["root"])
	t := BackupTarget{Prefix: string(data["prefix"])}
	if strings.TrimSpace(raw) == "" {
		return t
	}
	conf := rcloneSection(raw, "target")
	t.Configured = true
	switch conf["type"] {
	case "s3":
		t.Kind, t.Endpoint, t.Region, t.AccessKey = "s3", conf["endpoint"], conf["region"], conf["access_key_id"]
		t.Bucket, _, _ = strings.Cut(root, "/")
		t.Where = strings.TrimSuffix(t.Endpoint, "/") + "/" + root
	case "local":
		// The gateway sees the directory as /backup; it is localVolume's path.
		t.Kind, t.Where = "local", root+" on the first machine"
		if m := hostPath.FindStringSubmatch(string(data["localVolume"])); m != nil {
			t.Where = path.Join(m[1], strings.TrimPrefix(root, "/backup")) + " on the first machine"
		}
	default:
		t.Kind, t.Where = "other", conf["type"]+" remote, "+root
	}
	return t
}

// rcloneSection reads the options of one section of an rclone.conf.
func rcloneSection(conf, name string) map[string]string {
	out := map[string]string{}
	in := false
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#"), strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			in = line == "["+name+"]"
		case in:
			if k, v, ok := strings.Cut(line, "="); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// S3RcloneConf is the rclone.conf of an S3 target, as bootstrap writes it.
// The values come from a request: one with a line break or a space could
// add options of its own.
func S3RcloneConf(endpoint, accessKey, secretKey, region string) (string, error) {
	if region == "" {
		region = DefaultRegion
	}
	for name, v := range map[string]string{"endpoint": endpoint, "access key": accessKey, "secret key": secretKey, "region": region} {
		if v == "" || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return "", fmt.Errorf("the %s is empty or holds spaces or control characters", name)
		}
	}
	// Byte for byte bootstrap's (no final line break), so that a rerun of
	// it changes nothing.
	return fmt.Sprintf("[target]\ntype = s3\nprovider = Other\nendpoint = %s\naccess_key_id = %s\nsecret_access_key = %s\nregion = %s\ndirectory_markers = true",
		endpoint, accessKey, secretKey, region), nil
}
