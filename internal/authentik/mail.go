package authentik

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// System mail and invites (E-79). The cell sends mail through an SMTP
// server the admin sets; its settings live in one authentik email stage,
// which also sends the invites. An invite is authentik's recovery link of
// the person, on the invite flow deploy/base/identity.yaml declares.

const (
	// MailStage is the email stage that holds the cell's mail settings.
	MailStage = "cell-mail"
	// InviteFlow is the flow an invite link opens: set a password and a
	// second factor.
	InviteFlow = "cell-invite"
	// inviteTemplate is mounted from ConfigMap identity-mail-templates.
	inviteTemplate = "cell-invite.html"
)

// ErrNotPerson is returned for an invite to a machine account.
var ErrNotPerson = errors.New("only people get invites, not service accounts")

// Mail is the SMTP server the cell sends through; authentik never returns
// its password.
type Mail = cell.Mail

// GetMail returns the mail settings, or nil before an admin set them.
func (c *Client) GetMail(ctx context.Context) (*Mail, error) {
	s, err := c.find(ctx, "stages/email/", url.Values{"name": {MailStage}})
	if err != nil || s == nil {
		return nil, err
	}
	port, _ := s["port"].(float64)
	return &Mail{Host: fmt.Sprint(s["host"]), Port: int(port), Username: fmt.Sprint(s["username"]), From: fmt.Sprint(s["from_address"]), TLS: s["use_ssl"] == true}, nil
}

// SetMail creates or updates the mail stage.
func (c *Client) SetMail(ctx context.Context, m Mail) error {
	_, err := c.ensure(ctx, "stages/email/", "pk", url.Values{"name": {MailStage}}, map[string]any{
		"name":                MailStage,
		"use_global_settings": false,
		"host":                m.Host,
		"port":                m.Port,
		"username":            m.Username,
		"password":            m.Password,
		"use_tls":             !m.TLS,
		"use_ssl":             m.TLS,
		"timeout":             10,
		"from_address":        m.From,
		"subject":             "Your account is ready",
		"template":            inviteTemplate,
	})
	return err
}

// Invite is what an invite did: mailed the link, or returned it to hand over.
type Invite struct {
	Mailed  bool
	Link    string
	Expires time.Time
}

// InviteUser makes a link with which the person sets a password and a
// second factor, valid for the given time. With mail set and an address on
// file authentik mails it; otherwise the link is returned. A new invite
// replaces the person's last one. Superusers and machine accounts get none:
// the link signs in as that account.
func (c *Client) InviteUser(ctx context.Context, username string, valid time.Duration) (Invite, error) {
	if internalUser(username) {
		return Invite{}, fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	u, err := c.find(ctx, "core/users/", url.Values{"username": {username}})
	if err != nil {
		return Invite{}, err
	}
	if u == nil {
		return Invite{}, fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	if strings.Contains(fmt.Sprint(u["type"]), "service_account") {
		return Invite{}, fmt.Errorf("user %q: %w", username, ErrNotPerson)
	}
	if u["is_superuser"] == true {
		return Invite{}, fmt.Errorf("invite for %q: %w", username, ErrSuperuser)
	}
	if err := c.ensureInviteFlow(ctx); err != nil {
		return Invite{}, err
	}
	// authentik keeps the key of a recovery token it reissues and only moves
	// its expiry, so the old link would work on: revoke it first. Two
	// invites at once share the one new link: authentik keeps one token per
	// person, so no earlier link survives either way.
	if err := c.revokeInvite(ctx, u["uid"]); err != nil {
		return Invite{}, err
	}
	duration := fmt.Sprintf("minutes=%d", int(valid.Minutes()))
	inv := Invite{Expires: time.Now().Add(valid)}
	stage, err := c.find(ctx, "stages/email/", url.Values{"name": {MailStage}})
	if err != nil {
		return Invite{}, err
	}
	if email, _ := u["email"].(string); stage != nil && email != "" {
		inv.Mailed = true
		return inv, c.send(ctx, http.MethodPost, fmt.Sprintf("core/users/%v/recovery_email/", u["pk"]),
			map[string]any{"email_stage": stage["pk"], "token_duration": duration}, nil, true)
	}
	var res struct {
		Link string `json:"link"`
	}
	if err := c.send(ctx, http.MethodPost, fmt.Sprintf("core/users/%v/recovery/", u["pk"]), map[string]any{"token_duration": duration}, &res, true); err != nil {
		return Invite{}, err
	}
	// authentik builds the link from the request; one that points elsewhere
	// would not open for the person. The link is a credential: the error
	// names only where it points.
	if !strings.HasPrefix(res.Link, strings.TrimSuffix(c.PublicURL, "/")+"/") {
		where := "an unreadable address"
		if l, err := url.Parse(res.Link); err == nil {
			where = l.Scheme + "://" + l.Host
		}
		return Invite{}, fmt.Errorf("authentik: the invite link points at %s, not %s", where, c.PublicURL)
	}
	inv.Link = res.Link
	return inv, nil
}

// revokeInvite deletes the person's recovery token, if there is one.
func (c *Client) revokeInvite(ctx context.Context, uid any) error {
	if uid == nil || fmt.Sprint(uid) == "" {
		return fmt.Errorf("authentik: user without uid")
	}
	err := c.do(ctx, http.MethodDelete, "core/tokens/"+url.PathEscape(fmt.Sprintf("%v-password-reset", uid))+"/", nil, nil)
	var ak *Error
	if errors.As(err, &ak) && ak.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// ensureInviteFlow makes the invite flow the recovery flow of the default
// brand: authentik makes recovery links only on that flow.
func (c *Client) ensureInviteFlow(ctx context.Context) error {
	flow, err := c.pk(ctx, "flows/instances/", url.Values{"slug": {InviteFlow}})
	if err != nil {
		return err
	}
	brand, err := c.find(ctx, "core/brands/", url.Values{"default": {"true"}})
	if err != nil {
		return err
	}
	if brand == nil {
		return fmt.Errorf("authentik: no default brand")
	}
	if fmt.Sprint(brand["flow_recovery"]) == fmt.Sprint(flow) {
		return nil
	}
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("core/brands/%v/", brand["brand_uuid"]), map[string]any{"flow_recovery": flow}, nil)
}
