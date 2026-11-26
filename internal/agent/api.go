package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// TaskHeader names the task an agent's API call belongs to; the API records
// the call as a step of it (internal/api.TaskHeader).
const TaskHeader = "X-Cell-Task"

// API is the platform API as the agent's account sees it. The account logs
// in with username and app password (client credentials grant).
type API struct {
	URL      string // e.g. http://cell-api.cell-system.svc
	TokenURL string // the identity's token endpoint
	ClientID string // the API's client in the identity
	Username string
	Password string
	HTTP     *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (c *API) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.ClientID},
		"username": {c.Username}, "password": {c.Password}, "scope": {"openid profile"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("login as %s: HTTP %d: %s", c.Username, resp.StatusCode, body)
	}
	c.token = tok.AccessToken
	c.expires = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - time.Minute)
	return c.token, nil
}

// Do calls the API on behalf of task ("" outside a task) and returns status
// and body.
func (c *API) Do(ctx context.Context, method, path, task string, in any) (int, []byte, error) {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return 0, nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		token, err := c.bearer(ctx)
		if err != nil {
			return 0, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(payload))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if task != "" {
			req.Header.Set(TaskHeader, task)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return 0, nil, err
		}
		// The platform's own API bounds what it answers (logs, events); the
		// tools cap what reaches the model. A cap here would break lists.
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return 0, nil, err
		}
		// A token the identity revoked early: log in again, once.
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
			continue
		}
		return resp.StatusCode, body, nil
	}
}
