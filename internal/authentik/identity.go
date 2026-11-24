package authentik

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// People, groups and API access.

// ErrNotFound is returned when a named user or group does not exist.
var ErrNotFound = errors.New("not found")

// Duplicate reports whether a 400 answer means "already exists" (authentik
// words it either way, depending on the field).
func (e *Error) Duplicate() bool {
	body := strings.ToLower(e.Body)
	return e.Status == http.StatusBadRequest && (strings.Contains(body, "already exists") || strings.Contains(body, "must be unique"))
}

type User struct {
	Username, Name, Email string
	Groups                []string
}

type Group struct {
	Name    string
	Members []string
}

// list reads every page of a list endpoint.
func (c *Client) list(ctx context.Context, path string, query url.Values) ([]map[string]any, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("page_size", "100")
	var all []map[string]any
	for page := 1; page <= 1000; page++ {
		query.Set("page", fmt.Sprint(page))
		var res struct {
			Pagination struct {
				Next int `json:"next"`
			} `json:"pagination"`
			Results []map[string]any `json:"results"`
		}
		if err := c.do(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Results...)
		if res.Pagination.Next == 0 || len(res.Results) == 0 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("authentik: %s has more than 1000 pages", path)
}

// internalUser hides authentik's own accounts (outposts, anonymous).
func internalUser(username string) bool {
	return username == "AnonymousUser" || strings.HasPrefix(username, "ak-outpost-")
}

func names(objs any, key string) []string {
	list, _ := objs.([]any)
	var out []string
	for _, o := range list {
		if m, ok := o.(map[string]any); ok {
			out = append(out, fmt.Sprint(m[key]))
		}
	}
	return out
}

func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	objs, err := c.list(ctx, "core/users/", nil)
	if err != nil {
		return nil, err
	}
	var users []User
	for _, o := range objs {
		u := User{Username: fmt.Sprint(o["username"]), Name: fmt.Sprint(o["name"]), Email: fmt.Sprint(o["email"]), Groups: names(o["groups_obj"], "name")}
		if !internalUser(u.Username) {
			users = append(users, u)
		}
	}
	return users, nil
}

func (c *Client) userPK(ctx context.Context, username string) (any, error) {
	u, err := c.find(ctx, "core/users/", url.Values{"username": {username}})
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	return u["pk"], nil
}

func (c *Client) groupObj(ctx context.Context, name string) (map[string]any, error) {
	g, err := c.find(ctx, "core/groups/", url.Values{"name": {name}})
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, fmt.Errorf("group %q: %w", name, ErrNotFound)
	}
	return g, nil
}

// CreateUser creates a person (active, in the given groups) and sets an
// initial password if one is given.
func (c *Client) CreateUser(ctx context.Context, u User, password string) (User, error) {
	groups := []any{}
	for _, g := range u.Groups {
		obj, err := c.groupObj(ctx, g)
		if err != nil {
			return User{}, err
		}
		groups = append(groups, obj["pk"])
	}
	var created map[string]any
	if err := c.do(ctx, http.MethodPost, "core/users/", map[string]any{
		"username": u.Username, "name": u.Name, "email": u.Email, "is_active": true, "groups": groups,
	}, &created); err != nil {
		return User{}, err
	}
	if password != "" {
		if err := c.do(ctx, http.MethodPost, fmt.Sprintf("core/users/%v/set_password/", created["pk"]), map[string]any{"password": password}, nil); err != nil {
			return User{}, err
		}
	}
	return u, nil
}

// EnsureServiceAccount creates a machine account if it is missing (no
// password login; it logs in with app passwords) and adds it to groups.
func (c *Client) EnsureServiceAccount(ctx context.Context, username string, groups ...string) error {
	switch _, err := c.userPK(ctx, username); {
	case errors.Is(err, ErrNotFound):
		if err := c.do(ctx, http.MethodPost, "core/users/", map[string]any{
			"username": username, "name": username, "type": "service_account", "is_active": true,
		}, nil); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	for _, g := range groups {
		if err := c.EnsureGroup(ctx, g, username); err != nil {
			return err
		}
	}
	return nil
}

// CreateAppPassword creates a non-expiring app password for API logins
// (client credentials grant) and returns its key.
func (c *Client) CreateAppPassword(ctx context.Context, username string) (string, error) {
	pk, err := c.userPK(ctx, username)
	if err != nil {
		return "", err
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := "cell-api-" + username + "-" + hex.EncodeToString(b)
	if err := c.do(ctx, http.MethodPost, "core/tokens/", map[string]any{
		"identifier": id, "intent": "app_password", "user": pk, "expiring": false, "description": "cell platform API",
	}, nil); err != nil {
		return "", err
	}
	var key struct {
		Key string `json:"key"`
	}
	if err := c.do(ctx, http.MethodGet, "core/tokens/"+id+"/view_key/", nil, &key); err != nil {
		return "", err
	}
	return key.Key, nil
}

func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	objs, err := c.list(ctx, "core/groups/", url.Values{"include_users": {"true"}})
	if err != nil {
		return nil, err
	}
	var groups []Group
	for _, o := range objs {
		g := Group{Name: fmt.Sprint(o["name"])}
		for _, u := range names(o["users_obj"], "username") {
			if !internalUser(u) {
				g.Members = append(g.Members, u)
			}
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func (c *Client) CreateGroup(ctx context.Context, name string) (Group, error) {
	if err := c.do(ctx, http.MethodPost, "core/groups/", map[string]any{"name": name}, nil); err != nil {
		return Group{}, err
	}
	return Group{Name: name}, nil
}

// SetGroupMembers makes exactly the given users members of the group.
func (c *Client) SetGroupMembers(ctx context.Context, group string, usernames []string) (Group, error) {
	g, err := c.groupObj(ctx, group)
	if err != nil {
		return Group{}, err
	}
	pks := []any{}
	for _, u := range usernames {
		pk, err := c.userPK(ctx, u)
		if err != nil {
			return Group{}, err
		}
		pks = append(pks, pk)
	}
	if err := c.do(ctx, http.MethodPatch, fmt.Sprintf("core/groups/%v/", g["pk"]), map[string]any{"users": pks}, nil); err != nil {
		return Group{}, err
	}
	return Group{Name: group, Members: usernames}, nil
}

// ChangeGroupMembers adds and removes single users, each in one call of its
// own, and returns the group as it is afterwards.
func (c *Client) ChangeGroupMembers(ctx context.Context, group string, add, remove []string) (Group, error) {
	g, err := c.groupObj(ctx, group)
	if err != nil {
		return Group{}, err
	}
	for op, users := range map[string][]string{"add_user": add, "remove_user": remove} {
		for _, u := range users {
			pk, err := c.userPK(ctx, u)
			if err != nil {
				return Group{}, err
			}
			if err := c.do(ctx, http.MethodPost, fmt.Sprintf("core/groups/%v/%s/", g["pk"], op), map[string]any{"pk": pk}, nil); err != nil {
				return Group{}, err
			}
		}
	}
	groups, err := c.ListGroups(ctx)
	if err != nil {
		return Group{}, err
	}
	for _, g := range groups {
		if g.Name == group {
			return g, nil
		}
	}
	return Group{}, fmt.Errorf("group %q: %w", group, ErrNotFound)
}

// EnsureGroup creates a group if missing and adds the given users to it.
func (c *Client) EnsureGroup(ctx context.Context, name string, members ...string) error {
	if _, err := c.groupObj(ctx, name); errors.Is(err, ErrNotFound) {
		if _, err := c.CreateGroup(ctx, name); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if len(members) == 0 {
		return nil
	}
	groups, err := c.ListGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name != name {
			continue
		}
		have := map[string]bool{}
		for _, m := range g.Members {
			have[m] = true
		}
		missing := false
		for _, m := range members {
			missing = missing || !have[m]
			have[m] = true
		}
		if !missing {
			return nil
		}
		var all []string
		for m := range have {
			all = append(all, m)
		}
		_, err := c.SetGroupMembers(ctx, name, all)
		return err
	}
	return nil
}

// EnsureAPIClient registers the platform API as an OAuth2 client that
// issues JWTs to users who log in with username and app password
// (client credentials grant). Its issuer is <authentik>/application/o/<slug>/.
func (c *Client) EnsureAPIClient(ctx context.Context, slug, title string) error {
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
	providerPK, err := c.ensure(ctx, "providers/oauth2/", "pk", url.Values{"name": {slug}}, map[string]any{
		"name":               slug,
		"authorization_flow": authz,
		"invalidation_flow":  invalidation,
		"client_type":        "public",
		"client_id":          slug,
		"signing_key":        key,
		"property_mappings":  mappings,
		"redirect_uris":      []map[string]string{},
		"grant_types":        []string{"client_credentials"},
	})
	if err != nil {
		return err
	}
	return c.ensureApplication(ctx, slug, title, "", providerPK)
}

// EnsureAccess limits an application to members of the given groups. No
// groups means everyone may use it (authentik passes without bindings).
// New bindings come before old ones go, so a failure halfway leaves the
// application restricted, never open.
func (c *Client) EnsureAccess(ctx context.Context, slug string, groups []string) error {
	app, err := c.findApplication(ctx, slug)
	if err != nil {
		return err
	}
	if app == nil {
		return fmt.Errorf("application %q: %w", slug, ErrNotFound)
	}
	want := map[string]bool{}
	for _, name := range groups {
		g, err := c.groupObj(ctx, name)
		if err != nil {
			return err
		}
		want[fmt.Sprint(g["pk"])] = true
	}
	bindings, err := c.list(ctx, "policies/bindings/", url.Values{"target": {fmt.Sprint(app["pk"])}})
	if err != nil {
		return err
	}
	have := map[string]bool{}
	var obsolete []map[string]any
	for _, b := range bindings {
		if fmt.Sprint(b["target"]) != fmt.Sprint(app["pk"]) || b["group"] == nil {
			continue
		}
		if g := fmt.Sprint(b["group"]); want[g] {
			have[g] = true
		} else {
			obsolete = append(obsolete, b)
		}
	}
	order := len(bindings)
	for g := range want {
		if have[g] {
			continue
		}
		order++
		if err := c.do(ctx, http.MethodPost, "policies/bindings/", map[string]any{
			"target": app["pk"], "group": g, "order": order, "enabled": true, "negate": false, "timeout": 30,
		}, nil); err != nil {
			return err
		}
	}
	for _, b := range obsolete {
		if err := c.do(ctx, http.MethodDelete, fmt.Sprintf("policies/bindings/%v/", b["pk"]), nil, nil); err != nil {
			return err
		}
	}
	return nil
}
