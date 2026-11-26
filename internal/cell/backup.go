package cell

import (
	"fmt"
	"net/netip"
	"net/url"
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

// privateNetworks are the only places a backup target may be reached over
// plain HTTP, and only when asked for on purpose (E-68): RFC 1918,
// loopback, link-local, CGNAT and IPv6 unique local addresses.
var privateNetworks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("fc00::/7"),
}

// CheckBackupEndpoint lets an S3 endpoint through if it is https, or http
// with privateNetwork set and an address of a private network as its host.
// A host name never passes over http: what it resolves to is not known
// here and can change. The installer has the same rule (deploy/lib.sh,
// backup_endpoint_ok); both test testdata/endpoints.txt.
func CheckBackupEndpoint(endpoint string, privateNetwork bool) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("endpoint: not a URL")
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme != "http":
		return fmt.Errorf("endpoint: an https URL")
	case !privateNetwork:
		return fmt.Errorf("endpoint: https only; plain http only on a private network, asked for on purpose")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err == nil && ip.Zone() == "" {
		ip = ip.Unmap()
		for _, n := range privateNetworks {
			if n.Contains(ip) {
				return nil
			}
		}
	}
	return fmt.Errorf("endpoint: plain http only to an address of a private network (10/8, 172.16/12, 192.168/16, loopback, link-local, 100.64/10, fc00::/7), not to a host name")
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
		// Quotes, %(name)s and comment marks mean more than the value to
		// rclone's config parser: the file would differ from the check.
		if v == "" || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) || strings.ContainsAny(v, "\"'`%\\#;") {
			return "", fmt.Errorf("the %s is empty or holds spaces, control characters or any of \"'`%%\\#;", name)
		}
	}
	// Byte for byte bootstrap's (no final line break), so that a rerun of
	// it changes nothing.
	return fmt.Sprintf("[target]\ntype = s3\nprovider = Other\nendpoint = %s\naccess_key_id = %s\nsecret_access_key = %s\nregion = %s\ndirectory_markers = true",
		endpoint, accessKey, secretKey, region), nil
}
