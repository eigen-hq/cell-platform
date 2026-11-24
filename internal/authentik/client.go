// Package authentik is a minimal client for the authentik API: login clients
// (OIDC or forward-auth proxy) for apps.
package authentik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type Client struct {
	// BaseURL of authentik, e.g. http://authentik-server.cell-system.svc
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Error is a non-2xx answer of the API.
type Error struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *Error) Error() string {
	return fmt.Sprintf("authentik %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+"/api/v3/"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("authentik %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &Error{Method: method, Path: path, Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// find returns the object of a list endpoint whose fields equal the query,
// or nil. Filters are only a hint to the server (field and field__iexact):
// some endpoints silently ignore some filters, so every page is checked here
// for an exact match.
func (c *Client) find(ctx context.Context, path string, query url.Values) (map[string]any, error) {
	if path == "core/applications/" {
		return c.findApplication(ctx, query.Get("slug"))
	}
	hint := url.Values{"page_size": {"100"}}
	for k := range query {
		hint.Set(k, query.Get(k))
		hint.Set(k+"__iexact", query.Get(k))
	}
	for pageNo := 1; ; pageNo++ {
		hint.Set("page", fmt.Sprint(pageNo))
		var page struct {
			Pagination struct {
				Next int `json:"next"`
			} `json:"pagination"`
			Results []map[string]any `json:"results"`
		}
		if err := c.do(ctx, http.MethodGet, path+"?"+hint.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, obj := range page.Results {
			match := true
			for k := range query {
				if fmt.Sprint(obj[k]) != query.Get(k) {
					match = false
				}
			}
			if match {
				return obj, nil
			}
		}
		if page.Pagination.Next == 0 || len(page.Results) == 0 {
			return nil, nil
		}
		if pageNo >= 1000 {
			return nil, fmt.Errorf("authentik: %s has more than 1000 pages", path)
		}
	}
}

// findApplication looks an application up by slug (its detail URL).
func (c *Client) findApplication(ctx context.Context, slug string) (map[string]any, error) {
	if slug == "" {
		return nil, fmt.Errorf("authentik: application lookup without slug")
	}
	var app map[string]any
	err := c.do(ctx, http.MethodGet, "core/applications/"+url.PathEscape(slug)+"/", nil, &app)
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil, nil
	}
	return app, err
}

// ensure creates the object at the list endpoint or updates the existing one
// and returns its primary key. key is the field the detail URL uses (pk for
// most objects, slug for applications).
func (c *Client) ensure(ctx context.Context, path, key string, query url.Values, obj map[string]any) (any, error) {
	existing, err := c.find(ctx, path, query)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	switch {
	case existing == nil:
		err = c.do(ctx, http.MethodPost, path, obj, &out)
	case contains(existing, obj):
		// Unchanged: no write, so authentik does not refresh its outposts.
		return existing["pk"], nil
	default:
		err = c.do(ctx, http.MethodPatch, fmt.Sprintf("%s%v/", path, existing[key]), obj, &out)
	}
	if err != nil {
		return nil, err
	}
	return out["pk"], nil
}

// contains reports whether every field of want has the same JSON value in have.
func contains(have, want map[string]any) bool {
	for k, v := range want {
		a, errA := json.Marshal(v)
		b, errB := json.Marshal(have[k])
		if errA != nil || errB != nil || !bytes.Equal(normalize(a), normalize(b)) {
			return false
		}
	}
	return true
}

// normalize re-encodes JSON so that equal values compare equal bytewise:
// map keys sorted and list order ignored (every list here is a set).
func normalize(raw []byte) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, _ := json.Marshal(sortLists(v))
	return out
}

func sortLists(v any) any {
	switch t := v.(type) {
	case []any:
		items := make([]string, len(t))
		for i, x := range t {
			b, _ := json.Marshal(sortLists(x))
			items[i] = string(b)
		}
		sort.Strings(items)
		out := make([]any, len(items))
		for i, x := range items {
			out[i] = json.RawMessage(x)
		}
		return out
	case map[string]any:
		for k, x := range t {
			t[k] = sortLists(x)
		}
	}
	return v
}

func (c *Client) pk(ctx context.Context, path string, query url.Values) (any, error) {
	obj, err := c.find(ctx, path, query)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("authentik: nothing found at %s?%s", path, query.Encode())
	}
	return obj["pk"], nil
}

func (c *Client) flows(ctx context.Context) (authz, invalidation any, err error) {
	if authz, err = c.pk(ctx, "flows/instances/", url.Values{"slug": {"default-provider-authorization-implicit-consent"}}); err != nil {
		return nil, nil, err
	}
	invalidation, err = c.pk(ctx, "flows/instances/", url.Values{"slug": {"default-provider-invalidation-flow"}})
	return authz, invalidation, err
}

// OIDC describes an OpenID Connect client: confidential for an app, or
// public (no secret, authorization code with PKCE) for a client that runs on
// the user's device, such as the console.
type OIDC struct {
	Slug, Title, LaunchURL string
	ClientID, ClientSecret string
	RedirectURIs           []string
	Public                 bool
}

// EnsureOIDC creates or updates the provider and application of an app.
// The provider's issuer is <authentik>/application/o/<slug>/.
func (c *Client) EnsureOIDC(ctx context.Context, o OIDC) error {
	authz, invalidation, err := c.flows(ctx)
	if err != nil {
		return err
	}
	key, err := c.pk(ctx, "crypto/certificatekeypairs/", url.Values{"name": {"authentik Self-signed Certificate"}})
	if err != nil {
		return err
	}
	var mappings []any
	for _, scope := range []string{"openid", "email", "profile"} {
		pk, err := c.pk(ctx, "propertymappings/provider/scope/", url.Values{"managed": {"goauthentik.io/providers/oauth2/scope-" + scope}})
		if err != nil {
			return err
		}
		mappings = append(mappings, pk)
	}
	var redirects []map[string]string
	for _, u := range o.RedirectURIs {
		redirects = append(redirects, map[string]string{"matching_mode": "strict", "url": u, "redirect_uri_type": "authorization"})
	}
	provider := map[string]any{
		"name":               o.Slug,
		"authorization_flow": authz,
		"invalidation_flow":  invalidation,
		"client_type":        "confidential",
		"client_id":          o.ClientID,
		"client_secret":      o.ClientSecret,
		"redirect_uris":      redirects,
		"signing_key":        key,
		"property_mappings":  mappings,
		"grant_types":        []string{"authorization_code", "refresh_token"},
	}
	if o.Public {
		provider["client_type"] = "public"
		provider["grant_types"] = []string{"authorization_code"}
		delete(provider, "client_secret")
	}
	providerPK, err := c.ensure(ctx, "providers/oauth2/", "pk", url.Values{"name": {o.Slug}}, provider)
	if err != nil {
		return err
	}
	return c.ensureApplication(ctx, o.Slug, o.Title, o.LaunchURL, providerPK)
}

// Proxy describes forward-auth protection for an app without own login.
type Proxy struct {
	Slug, Title, ExternalHost string
	// AuthentikURL is the public URL of authentik, used by the outpost for redirects.
	AuthentikURL string
}

// EnsureProxy creates or updates a forward-auth provider and application and
// binds it to the embedded outpost.
func (c *Client) EnsureProxy(ctx context.Context, p Proxy) error {
	authz, invalidation, err := c.flows(ctx)
	if err != nil {
		return err
	}
	providerPK, err := c.ensure(ctx, "providers/proxy/", "pk", url.Values{"name": {p.Slug}}, map[string]any{
		"name":               p.Slug,
		"authorization_flow": authz,
		"invalidation_flow":  invalidation,
		"external_host":      p.ExternalHost,
		"mode":               "forward_single",
	})
	if err != nil {
		return err
	}
	if err := c.ensureApplication(ctx, p.Slug, p.Title, p.ExternalHost, providerPK); err != nil {
		return err
	}
	return c.updateOutpost(ctx, p.AuthentikURL, func(providers []any) []any {
		for _, x := range providers {
			if fmt.Sprint(x) == fmt.Sprint(providerPK) {
				return providers
			}
		}
		return append(providers, providerPK)
	})
}

// ProxyClientID returns the OAuth2 client ID of an app's forward-auth
// provider; the outpost redirects to the login with it.
func (c *Client) ProxyClientID(ctx context.Context, slug string) (string, error) {
	p, err := c.find(ctx, "providers/proxy/", url.Values{"name": {slug}})
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", fmt.Errorf("authentik: no proxy provider %q", slug)
	}
	return fmt.Sprint(p["client_id"]), nil
}

func (c *Client) updateOutpost(ctx context.Context, authentikURL string, change func([]any) []any) error {
	outpost, err := c.find(ctx, "outposts/instances/", url.Values{"managed": {"goauthentik.io/outposts/embedded"}})
	if err != nil {
		return err
	}
	if outpost == nil {
		return fmt.Errorf("authentik: embedded outpost not found")
	}
	providers, _ := outpost["providers"].([]any)
	config, _ := outpost["config"].(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	before, _ := json.Marshal(map[string]any{"providers": providers, "config": config})
	if authentikURL != "" {
		config["authentik_host"] = authentikURL
	}
	patch := map[string]any{"providers": change(providers), "config": config}
	if after, _ := json.Marshal(patch); bytes.Equal(normalize(before), normalize(after)) {
		return nil // unchanged: a write would make the outpost reload
	}
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("outposts/instances/%v/", outpost["pk"]), patch, nil)
}

func (c *Client) ensureApplication(ctx context.Context, slug, title, launchURL string, providerPK any) error {
	if title == "" {
		title = slug
	}
	_, err := c.ensure(ctx, "core/applications/", "slug", url.Values{"slug": {slug}}, map[string]any{
		"name":            title,
		"slug":            slug,
		"provider":        providerPK,
		"meta_launch_url": launchURL,
	})
	return err
}

// Delete removes the application and provider of an app. Missing objects
// are fine.
func (c *Client) Delete(ctx context.Context, slug string) error {
	app, err := c.find(ctx, "core/applications/", url.Values{"slug": {slug}})
	if err != nil {
		return err
	}
	if app != nil {
		if err := c.do(ctx, http.MethodDelete, "core/applications/"+slug+"/", nil, nil); err != nil {
			return err
		}
	}
	if p, err := c.find(ctx, "providers/proxy/", url.Values{"name": {slug}}); err != nil {
		return err
	} else if p != nil {
		if err := c.updateOutpost(ctx, "", func(providers []any) []any {
			keep := []any{} // never null: authentik rejects it
			for _, x := range providers {
				if fmt.Sprint(x) != fmt.Sprint(p["pk"]) {
					keep = append(keep, x)
				}
			}
			return keep
		}); err != nil {
			return err
		}
		if err := c.do(ctx, http.MethodDelete, fmt.Sprintf("providers/proxy/%v/", p["pk"]), nil, nil); err != nil {
			return err
		}
	}
	if p, err := c.find(ctx, "providers/oauth2/", url.Values{"name": {slug}}); err != nil {
		return err
	} else if p != nil {
		return c.do(ctx, http.MethodDelete, fmt.Sprintf("providers/oauth2/%v/", p["pk"]), nil, nil)
	}
	return nil
}
