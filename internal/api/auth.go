package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Tokens checks bearer tokens with the identity. authentik's userinfo
// endpoint accepts only tokens it issued and still holds, and answers with
// the user's current groups; the token's audience must be one of the
// platform's own clients (the API's and those of its first-party clients
// such as the console), so a token an app got for its users does not work
// here.
type Tokens struct {
	UserinfoURL string
	ClientIDs   []string
	HTTP        *http.Client
	// TTL of a checked token in the cache; group changes take this long.
	TTL time.Duration
	Now func() time.Time

	mu    sync.Mutex
	cache map[[32]byte]cachedCaller
}

type cachedCaller struct {
	caller  Caller
	expires time.Time
}

func (t *Tokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Check returns the caller a token belongs to.
func (t *Tokens) Check(ctx context.Context, token string) (*Caller, error) {
	key := sha256.Sum256([]byte(token))
	now := t.now()
	t.mu.Lock()
	if c, ok := t.cache[key]; ok && now.Before(c.expires) {
		t.mu.Unlock()
		caller := c.caller
		return &caller, nil
	}
	t.mu.Unlock()

	// The claims are read before the identity vouches for the token, but
	// only used after it did: they belong to a token it issued.
	exp, iat, err := t.audience(token)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.UserinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("identity unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the identity does not accept it (HTTP %d)", resp.StatusCode)
	}
	var info struct {
		Username string   `json:"preferred_username"`
		Groups   []string `json:"groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	if info.Username == "" {
		return nil, errors.New("userinfo has no preferred_username")
	}
	caller := Caller{Username: info.Username, Groups: info.Groups, Role: RoleOf(info.Groups), IssuedAt: iat}
	if caller.Groups == nil {
		caller.Groups = []string{}
	}
	expires := now.Add(t.TTL)
	if exp.Before(expires) {
		expires = exp
	}
	t.mu.Lock()
	if t.cache == nil || len(t.cache) > 10000 {
		t.cache = map[[32]byte]cachedCaller{} // ponytail: drop all instead of LRU; refill costs one userinfo call per token
	}
	t.cache[key] = cachedCaller{caller: caller, expires: expires}
	t.mu.Unlock()
	return &caller, nil
}

// audience checks that the token is a JWT for one of the clients and returns
// when it expires and when it was issued.
func (t *Tokens) audience(token string) (exp, iat time.Time, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return exp, iat, errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return exp, iat, errors.New("not a JWT")
	}
	var claims struct {
		Aud any     `json:"aud"`
		Exp float64 `json:"exp"`
		Iat float64 `json:"iat"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return exp, iat, errors.New("not a JWT")
	}
	var aud []string
	switch a := claims.Aud.(type) {
	case string:
		aud = []string{a}
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok {
				aud = append(aud, s)
			}
		}
	}
	if !slices.ContainsFunc(aud, func(a string) bool { return slices.Contains(t.ClientIDs, a) }) {
		return exp, iat, fmt.Errorf("token is for %v, not for %v", aud, t.ClientIDs)
	}
	return time.Unix(int64(claims.Exp), 0), time.Unix(int64(claims.Iat), 0), nil
}
