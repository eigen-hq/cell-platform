package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/smtp"
	"slices"
	"strings"
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
	// Own: the cell sends through its own mail server (E-84).
	Own       bool           `json:"own"`
	OwnServer *ownServerView `json:"ownServer,omitempty"`
}

// ownServerView is the cell's own mail server: its sender and the DNS
// records it needs, each looked up now.
type ownServerView struct {
	From    string      `json:"from"`
	Records []dnsRecord `json:"records"`
}

type dnsRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Set   bool   `json:"set"`
}

// Resolver looks up the own mail server's DNS records and its receivers'
// mail servers; *net.Resolver.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

func (s *Server) resolver() Resolver {
	if s.DNS == nil {
		return net.DefaultResolver
	}
	return s.DNS
}

// ownMail is the cell's own mail server from ConfigMap cell.OwnMail, nil
// on a cell without one.
type ownMail struct{ domain, address, selector, dkim string }

func (s *Server) ownMail(ctx context.Context) (*ownMail, error) {
	var cm corev1.ConfigMap
	err := s.Kube.Get(ctx, client.ObjectKey{Namespace: cell.SystemNamespace, Name: cell.OwnMail}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o := &ownMail{cm.Data["domain"], cm.Data["address"], cm.Data["selector"], cm.Data["dkim"]}
	ip, err := netip.ParseAddr(o.address)
	if o.domain == "" || err != nil || o.selector == "" || o.dkim == "" {
		return nil, fmt.Errorf("configmap %s: domain, address, selector and dkim are needed", cell.OwnMail)
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return nil, fmt.Errorf("configmap %s: %s is no public address; receivers would never match it", cell.OwnMail, o.address)
	}
	return o, nil
}

func (o *ownMail) from() string { return "cell@" + o.domain }

// records are the DNS records the own server needs for receivers to take
// its mail: SPF and DKIM for the sender's domain, DMARC, and the server's
// name (A or AAAA, and PTR back) matching its greeting.
func (o *ownMail) records(ctx context.Context, dns Resolver) []dnsRecord {
	txt := func(name string, ok func(string) bool) bool {
		vals, _ := dns.LookupTXT(ctx, name)
		return slices.ContainsFunc(vals, ok)
	}
	spf, a := "v=spf1 ip4:"+o.address+" -all", "A"
	if strings.Contains(o.address, ":") {
		spf, a = "v=spf1 ip6:"+o.address+" -all", "AAAA"
	}
	key := "v=DKIM1; k=rsa; p=" + o.dkim
	host := "mail." + o.domain
	names, _ := dns.LookupAddr(ctx, o.address)
	addrs, _ := dns.LookupHost(ctx, host)
	return []dnsRecord{
		{"TXT", o.domain, spf, txt(o.domain, func(v string) bool {
			return strings.HasPrefix(v, "v=spf1 ") && slices.Contains(strings.Fields(v), strings.Fields(spf)[1])
		})},
		{"TXT", o.selector + "._domainkey." + o.domain, key, txt(o.selector+"._domainkey."+o.domain, func(v string) bool {
			return strings.Contains(strings.ReplaceAll(v, " ", ""), "p="+o.dkim)
		})},
		{"TXT", "_dmarc." + o.domain, "v=DMARC1; p=quarantine", txt("_dmarc."+o.domain, func(v string) bool { return strings.HasPrefix(v, "v=DMARC1") })},
		{a, host, o.address, slices.ContainsFunc(addrs, func(v string) bool {
			p, err := netip.ParseAddr(v)
			return err == nil && p.String() == netip.MustParseAddr(o.address).String()
		})},
		{"PTR", o.address, host, slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(strings.TrimSuffix(n, "."), host) })},
	}
}

func (s *Server) mailView(ctx context.Context, m *authentik.Mail) (mailView, error) {
	v := mailView{}
	if m != nil {
		v = mailView{Configured: true, Host: m.Host, Port: m.Port, Username: m.Username, From: m.From, TLS: m.TLS, Own: m.Own()}
	}
	o, err := s.ownMail(ctx)
	if err != nil || o == nil {
		return v, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v.OwnServer = &ownServerView{From: o.from(), Records: o.records(ctx, s.resolver())}
	return v, nil
}

func (s *Server) getMail(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	m, err := s.Identity.GetMail(r.Context())
	if err != nil {
		return identityError(err)
	}
	v, err := s.mailView(r.Context(), m)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
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
		Own      bool   `json:"own"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Own {
		return s.setOwnMail(w, r, req.Host != "" || req.Port != 0 || req.Username != "" || req.Password != "" || req.From != "" || req.TLS)
	}
	if req.Host == cell.OwnMailHost {
		return errorf(http.StatusBadRequest, "host: the cell's own mail server is set with own")
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
	// Secret cell-mail is the one truth (E-82): the operator sets the
	// identity's mail stage from it, which sends invites, and mails warnings
	// with it. The last admin to save wins in both.
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: cell.MailSettings}}
	// An empty password keeps the one set before for the same server and
	// login; it never goes to another server.
	if req.Username != "" && req.Password == "" {
		if err := s.Kube.Get(r.Context(), client.ObjectKeyFromObject(secret), secret); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if string(secret.Data["host"]) == req.Host && string(secret.Data["username"]) == req.Username {
			req.Password = string(secret.Data["password"])
		}
	}
	if (req.Username == "") != (req.Password == "") {
		return errorf(http.StatusBadRequest, "password: the login's password, needed again for a new server or login")
	}
	m := authentik.Mail{Host: req.Host, Port: req.Port, Username: req.Username, Password: req.Password, From: req.From, TLS: req.TLS}
	check := s.CheckMail
	if check == nil {
		check = checkSMTP
	}
	if err := check(r.Context(), m); err != nil {
		return errorf(http.StatusBadRequest, "mail server check failed: %v", err)
	}
	return s.saveMail(w, r, m)
}

// setOwnMail sends through the cell's own mail server (E-84), once it
// takes the sender and the cell reaches the mail servers of its people on
// port 25: without that its mail would only wait in its queue.
func (s *Server) setOwnMail(w http.ResponseWriter, r *http.Request, more bool) error {
	if more {
		return errorf(http.StatusBadRequest, "own: the cell's own mail server takes no host, port, login, sender or tls")
	}
	o, err := s.ownMail(r.Context())
	if err != nil {
		return err
	}
	if o == nil {
		return errorf(http.StatusBadRequest, "this cell has no mail server of its own")
	}
	m := authentik.Mail{Host: cell.OwnMailHost, Port: cell.OwnMailPort, From: o.from()}
	check := s.CheckMail
	if check == nil {
		check = checkSMTP
	}
	if err := check(r.Context(), m); err != nil {
		return errorf(http.StatusBadRequest, "the cell's mail server check failed: %v", err)
	}
	users, err := s.Identity.ListUsers(r.Context())
	if err != nil {
		return identityError(err)
	}
	var domains []string
	for _, u := range users {
		if _, d, ok := strings.Cut(u.Email, "@"); ok && !slices.Contains(domains, strings.ToLower(d)) {
			domains = append(domains, strings.ToLower(d))
		}
	}
	if len(domains) == 0 {
		return errorf(http.StatusBadRequest, "give a person a mail address first: the check tries to reach their mail server")
	}
	reach := s.CheckDelivery
	if reach == nil {
		reach = func(ctx context.Context, domains []string) error {
			return checkDelivery(ctx, s.resolver(), (&net.Dialer{}).DialContext, domains)
		}
	}
	if err := reach(r.Context(), domains); err != nil {
		return errorf(http.StatusBadRequest, "the cell cannot deliver mail: %v", err)
	}
	return s.saveMail(w, r, m)
}

// checkDelivery connects to port 25 of the domains' mail servers, in their
// order, and reads a greeting; it sends nothing. A domain without MX
// records takes mail at its own address (RFC 5321 5.1); a null MX none.
func checkDelivery(ctx context.Context, dns Resolver, dial func(ctx context.Context, network, addr string) (net.Conn, error), domains []string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var tried []string
	for _, d := range domains {
		var hosts []string
		mxs, err := dns.LookupMX(ctx, d)
		var dnsErr *net.DNSError
		switch {
		case err == nil:
			for _, mx := range mxs {
				if h := strings.TrimSuffix(mx.Host, "."); h != "" {
					hosts = append(hosts, h)
				}
			}
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			hosts = []string{d}
		}
		for _, host := range hosts {
			tried = append(tried, host)
			if greets(ctx, dial, host) {
				return nil
			}
		}
	}
	if len(tried) == 0 {
		return fmt.Errorf("no mail server found for %s", strings.Join(domains, ", "))
	}
	return fmt.Errorf("port 25 of %s is not reachable from the cell; many hosting providers block it until asked", strings.Join(tried, ", "))
}

// greets reports whether host answers on port 25 with an SMTP greeting.
func greets(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), host string) bool {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort(host, "25"))
	if err != nil {
		return false
	}
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		_ = conn.Close()
		return false
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return false
	}
	_ = c.Close()
	return true
}

// saveMail writes Secret cell-mail and answers with the settings.
func (s *Server) saveMail(w http.ResponseWriter, r *http.Request, m authentik.Mail) error {
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
	v, err := s.mailView(r.Context(), &m)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
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
