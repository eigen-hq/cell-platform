package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// The backup target is no app, so it is set without a ChangeRequest: the
// operator first checks that it can write to it (like createJoin, a call of
// an admin with an immediate effect). The values end up in rclone.conf,
// the gateway's auth proxy and a shell, so only plain ones pass.
type backupTargetRequest struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Prefix    string `json:"prefix,omitempty"`
}

var (
	bucketName  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionName  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	prefixPart  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	credentials = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`) // printable, no spaces
)

func (q *backupTargetRequest) check() error {
	u, err := url.Parse(q.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		len(q.Endpoint) > 256 || strings.ContainsFunc(q.Endpoint, func(r rune) bool { return r <= ' ' || r >= 0x7f }) {
		return fmt.Errorf("endpoint: an http or https URL with a host only, such as https://fsn1.your-objectstorage.com")
	}
	q.Endpoint = strings.TrimSuffix(q.Endpoint, "/")
	if q.Region == "" {
		q.Region = cell.DefaultRegion
	}
	if !regionName.MatchString(q.Region) {
		return fmt.Errorf("region: lowercase letters, digits and -, such as fsn1")
	}
	if !bucketName.MatchString(q.Bucket) || strings.Contains(q.Bucket, "..") {
		return fmt.Errorf("bucket: 3 to 63 lowercase letters, digits, . and -")
	}
	if !credentials.MatchString(q.AccessKey) || !credentials.MatchString(q.SecretKey) {
		return fmt.Errorf("accessKey and secretKey: 1 to 256 printable characters without spaces")
	}
	if q.Prefix != "" {
		for _, p := range strings.Split(q.Prefix, "/") {
			if !prefixPart.MatchString(p) || p == "." || p == ".." {
				return fmt.Errorf("prefix: a path of letters, digits, . _ and -, without empty parts or .. (such as backups/cell)")
			}
		}
		if len(q.Prefix) > 200 {
			return fmt.Errorf("prefix: at most 200 characters")
		}
	}
	return nil
}

func (s *Server) getBackupTarget(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	t, err := s.backupTarget(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, t)
	return nil
}

// backupTarget reads what the operator tells about the target. Until it
// told anything, the cell has none.
func (s *Server) backupTarget(ctx context.Context) (cell.BackupTarget, error) {
	var t cell.BackupTarget
	var cm corev1.ConfigMap
	err := s.Kube.Get(ctx, client.ObjectKey{Namespace: cell.SystemNamespace, Name: cell.BackupTargetStatus}, &cm)
	if apierrors.IsNotFound(err) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(cm.Data[cell.BackupTargetStatusKey]), &t); err != nil {
		return t, fmt.Errorf("backup target status: %w", err)
	}
	return t, nil
}

// setBackupTarget asks the operator to check a new S3 target and use it if
// the check passes. One check at a time: the request has a fixed name.
func (s *Server) setBackupTarget(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var q backupTargetRequest
	if err := readJSON(r, &q); err != nil {
		return err
	}
	if err := q.check(); err != nil {
		return errorf(http.StatusBadRequest, "%v", err)
	}
	// Milliseconds: the console tells this check from an earlier one by it.
	at := s.now().UTC().Truncate(time.Millisecond)
	err := s.Kube.Create(r.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cell.BackupTargetRequest, Namespace: cell.SystemNamespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"endpoint": q.Endpoint, "region": q.Region, "bucket": q.Bucket, "prefix": q.Prefix,
			"accessKey": q.AccessKey, "secretKey": q.SecretKey,
			"requester": c.Username, "requestedAt": at.Format(time.RFC3339Nano),
		},
	})
	if apierrors.IsAlreadyExists(err) {
		return errorf(http.StatusConflict, "a backup target is being checked; wait for its result")
	}
	if err != nil {
		return err
	}
	t, err := s.backupTarget(r.Context())
	if err != nil {
		return err
	}
	prefix := q.Prefix
	if prefix == "" {
		prefix = t.Prefix
	}
	t.Check = &cell.BackupCheck{State: cell.CheckRunning, Target: q.Endpoint + "/" + strings.TrimSuffix(q.Bucket+"/"+prefix, "/"), At: at, By: c.Username}
	writeJSON(w, http.StatusAccepted, t)
	return nil
}
