// Package names gives cells an address of their own under a zone we run
// (E-55): a cell registers once and gets <name>-<random>.<zone> with a
// wildcard for its apps, and proves its certificates through TXT records
// the service writes for it. It speaks the acme-dns API at /update, which
// cert-manager's acmeDNS solver uses, so no webhook is needed.
//
// The service keeps no state: a cell's token is an HMAC of its label, and
// whether a label exists is what the DNS says.
package names

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
)

// DNS is the zone's record store. Names are relative to the zone; every
// record set belongs to a cell (its label).
type DNS interface {
	// Get returns the values of a record set, nil if there is none.
	Get(ctx context.Context, name, typ string) ([]string, error)
	// Set replaces a record set, creating it for the cell if needed, and
	// returns once the DNS has taken it.
	Set(ctx context.Context, cell, name, typ string, values []string, ttl int) error
	// Owned lists the names of the cell's record sets of a type.
	Owned(ctx context.Context, cell, typ string) ([]string, error)
}

type Server struct {
	Zone string
	// Keys allow registering a cell; we hand them out.
	Keys []string
	// Secret signs the cells' tokens.
	Secret []byte
	DNS    DNS
	Rand   io.Reader
	Log    *slog.Logger

	// ponytail: one lock for all cells (one replica, a few cells); per-cell
	// locks if writes ever queue.
	mu sync.Mutex
}

var (
	nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)
	// A cell's label: its name and a random part.
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?-[a-z0-9]{6}$`)
	// A host below a cell's domain, as cert-manager names it ("" is the cell
	// domain itself): one DNS label, like every host of a cell.
	subRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// An ACME DNS-01 key authorization digest: base64url SHA-256.
	txtRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

const (
	addressTTL   = 300
	challengeTTL = 60
)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("POST /v0/cells", s.register)
	mux.HandleFunc("PUT /v0/cells/{label}", s.move)
	mux.HandleFunc("POST /update", s.update)
	return mux
}

// Token is the secret of a cell's label.
func (s *Server) Token(label string) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte(label))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) authorized(label, token string) bool {
	return labelRe.MatchString(label) && hmac.Equal([]byte(token), []byte(s.Token(label)))
}

type cellRequest struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type cellResponse struct {
	Label  string `json:"label"`
	Domain string `json:"domain"`
	Token  string `json:"token"`
}

// register makes a new label for a cell and points it and its wildcard at
// the cell's address.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.knownKey(key) {
		fail(w, http.StatusUnauthorized, "a registration key is needed")
		return
	}
	var req cellRequest
	if !decode(w, r, &req) {
		return
	}
	if !nameRe.MatchString(req.Name) {
		fail(w, http.StatusBadRequest, "name: lowercase letters, digits and inner dashes, at most 30")
		return
	}
	addr, ok := address(req.Address)
	if !ok {
		fail(w, http.StatusBadRequest, "address: an IPv4 address")
		return
	}
	for range 5 {
		label := req.Name + "-" + s.random(6)
		taken, err := s.DNS.Get(r.Context(), label, "A")
		if err != nil {
			s.internal(w, err)
			return
		}
		if taken != nil {
			continue
		}
		if err := s.point(r.Context(), label, addr, []string{label, "*." + label}); err != nil {
			s.internal(w, err)
			return
		}
		s.log().Info("registered", "label", label)
		writeJSON(w, http.StatusCreated, cellResponse{Label: label, Domain: label + "." + s.Zone, Token: s.Token(label)})
		return
	}
	fail(w, http.StatusConflict, "no free label found, try again")
}

// move points a cell's names at a new address.
func (s *Server) move(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	label := r.PathValue("label")
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.authorized(label, token) {
		fail(w, http.StatusUnauthorized, "wrong token for this cell")
		return
	}
	var req cellRequest
	if !decode(w, r, &req) {
		return
	}
	addr, ok := address(req.Address)
	if !ok {
		fail(w, http.StatusBadRequest, "address: an IPv4 address")
		return
	}
	owned, err := s.DNS.Owned(r.Context(), label, "A")
	if err != nil {
		s.internal(w, err)
		return
	}
	if len(owned) == 0 {
		fail(w, http.StatusNotFound, "no such cell")
		return
	}
	if err := s.point(r.Context(), label, addr, owned); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cellResponse{Label: label, Domain: label + "." + s.Zone})
}

// point sets the cell's address records.
func (s *Server) point(ctx context.Context, label, addr string, names []string) error {
	for _, name := range names {
		if err := s.DNS.Set(ctx, label, name, "A", []string{addr}, addressTTL); err != nil {
			return err
		}
	}
	return nil
}

// update is the acme-dns call of cert-manager: X-Api-User is the cell's
// label, X-Api-Key its token, subdomain the host below the cell domain.
// Each host of a cell has a certificate of its own, so one challenge per
// record is enough: the newest replaces the one before.
// ponytail: a certificate for a host and its wildcard would need two values
// at once, with their order tracked outside the record set.
func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	// A challenge reads the cell's address and writes it to its host: a
	// move in between must not leave the host on the old one.
	s.mu.Lock()
	defer s.mu.Unlock()
	label := r.Header.Get("X-Api-User")
	if !s.authorized(label, r.Header.Get("X-Api-Key")) {
		fail(w, http.StatusUnauthorized, "wrong token for this cell")
		return
	}
	var req struct {
		Subdomain string `json:"subdomain"`
		TXT       string `json:"txt"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Subdomain != "" && !subRe.MatchString(req.Subdomain) {
		fail(w, http.StatusBadRequest, "subdomain: a host below the cell domain")
		return
	}
	if !txtRe.MatchString(req.TXT) {
		fail(w, http.StatusBadRequest, "txt: an ACME challenge digest")
		return
	}
	name := "_acme-challenge." + label
	if req.Subdomain != "" {
		name = "_acme-challenge." + req.Subdomain + "." + label
		// The challenge record makes the host a node of its own, which the
		// wildcard no longer answers (RFC 4592): it gets its own address.
		addr, err := s.DNS.Get(r.Context(), label, "A")
		if err != nil {
			s.internal(w, err)
			return
		}
		if addr == nil {
			fail(w, http.StatusNotFound, "no such cell")
			return
		}
		if err := s.DNS.Set(r.Context(), label, req.Subdomain+"."+label, "A", addr, addressTTL); err != nil {
			s.internal(w, err)
			return
		}
	}
	if err := s.DNS.Set(r.Context(), label, name, "TXT", []string{`"` + req.TXT + `"`}, challengeTTL); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"txt": req.TXT})
}

func (s *Server) knownKey(key string) bool {
	for _, k := range s.Keys {
		if k != "" && hmac.Equal([]byte(key), []byte(k)) {
			return true
		}
	}
	return false
}

// random returns n characters of [a-z0-9].
func (s *Server) random(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	src := s.Rand
	if src == nil {
		src = rand.Reader
	}
	if _, err := io.ReadFull(src, b); err != nil {
		panic(fmt.Sprintf("random: %v", err)) // crypto/rand does not fail
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// address accepts an IPv4 address, also a private one: a cell inside a
// company network gets its name, and its certificate through DNS.
func address(s string) (string, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
		return "", false
	}
	return a.String(), true
}

func (s *Server) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.log().Error("dns", "error", err)
	fail(w, http.StatusBadGateway, "the DNS did not take the change")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

