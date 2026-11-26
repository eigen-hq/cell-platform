package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/authentik"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// System mail and invites (E-79): the cell sends through an SMTP server the
// admin sets, checked before it is taken.

// inviteValid is how long an invite link works.
const inviteValid = 7 * 24 * time.Hour

type mailView struct {
	Configured bool   `json:"configured"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Username   string `json:"username,omitempty"`
	From       string `json:"from,omitempty"`
	TLS        bool   `json:"tls"`
}

func (s *Server) getMail(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	m, err := s.Identity.GetMail(r.Context())
	if err != nil {
		return identityError(err)
	}
	if m == nil {
		writeJSON(w, http.StatusOK, mailView{})
		return nil
	}
	writeJSON(w, http.StatusOK, mailView{Configured: true, Host: m.Host, Port: m.Port, Username: m.Username, From: m.From, TLS: m.TLS})
	return nil
}

func (s *Server) setMail(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		From     string `json:"from"`
		TLS      bool   `json:"tls"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Port == 0 {
		req.Port = 587
		if req.TLS {
			req.Port = 465
		}
	}
	if req.Host == "" || net.ParseIP(req.Host) == nil && !validHost(req.Host) {
		return errorf(http.StatusBadRequest, "host: a host name or IP address")
	}
	if req.Port < 1 || req.Port > 65535 {
		return errorf(http.StatusBadRequest, "port: 1 to 65535")
	}
	if a, err := mail.ParseAddress(req.From); err != nil || a.Address != req.From {
		return errorf(http.StatusBadRequest, "from: a plain address such as cell@example.com")
	}
	if (req.Username == "") != (req.Password == "") {
		return errorf(http.StatusBadRequest, "username and password go together")
	}
	m := authentik.Mail{Host: req.Host, Port: req.Port, Username: req.Username, Password: req.Password, From: req.From, TLS: req.TLS}
	check := s.CheckMail
	if check == nil {
		check = checkSMTP
	}
	if err := check(r.Context(), m); err != nil {
		return errorf(http.StatusBadRequest, "mail server check failed: %v", err)
	}
	// Secret cell-mail is the one truth (E-82): the operator sets the
	// identity's mail stage from it, which sends invites, and mails warnings
	// with it. The last admin to save wins in both.
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: cell.MailSettings}}
	patch, err := json.Marshal(map[string]any{"data": m.Data()})
	if err != nil {
		return err
	}
	err = s.Kube.Patch(r.Context(), secret, client.RawPatch(types.MergePatchType, patch))
	if apierrors.IsNotFound(err) {
		secret.Type, secret.Data = corev1.SecretTypeOpaque, m.Data()
		err = s.Kube.Create(r.Context(), secret)
		if apierrors.IsAlreadyExists(err) {
			err = s.Kube.Patch(r.Context(), secret, client.RawPatch(types.MergePatchType, patch))
		}
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, mailView{Configured: true, Host: m.Host, Port: m.Port, Username: m.Username, From: m.From, TLS: m.TLS})
	return nil
}

func validHost(h string) bool {
	if len(h) > 253 {
		return false
	}
	for _, c := range h {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// checkSMTP connects with TLS, logs in and has the sender accepted; it
// sends nothing.
func checkSMTP(ctx context.Context, m authentik.Mail) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := m.Dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("sender %s: %w", m.From, err)
	}
	return c.Quit()
}

type inviteView struct {
	Mailed  bool      `json:"mailed"`
	Link    string    `json:"link,omitempty"`
	Expires time.Time `json:"expires"`
}

// inviteUser makes an invite link. It is a credential until it expires, so
// it is never logged.
func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	inv, err := s.Identity.InviteUser(r.Context(), r.PathValue("name"), inviteValid)
	if errors.Is(err, authentik.ErrNotPerson) {
		return errorf(http.StatusBadRequest, "%v", err)
	}
	if err != nil {
		return identityError(err)
	}
	writeJSON(w, http.StatusCreated, inviteView{Mailed: inv.Mailed, Link: inv.Link, Expires: inv.Expires.UTC()})
	return nil
}
