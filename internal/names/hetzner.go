package names

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Hetzner is the zone in the Hetzner Cloud DNS (api.hetzner.cloud/v1).
type Hetzner struct {
	BaseURL string // default https://api.hetzner.cloud/v1
	Token   string
	Zone    string
	HTTP    *http.Client
	// Poll is the pause between looks at a running action (default 1 s).
	Poll time.Duration
}

// action is Hetzner's handle on a change, which runs asynchronously.
type action struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type record struct {
	Value string `json:"value"`
}

func (h *Hetzner) Get(ctx context.Context, name, typ string) ([]string, error) {
	var out struct {
		RRSet struct {
			Records []record `json:"records"`
		} `json:"rrset"`
	}
	code, err := h.call(ctx, http.MethodGet, h.rrset(name, typ), nil, &out)
	if code == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	values := []string{}
	for _, r := range out.RRSet.Records {
		values = append(values, r.Value)
	}
	return values, nil
}

func (h *Hetzner) Set(ctx context.Context, cell, name, typ string, values []string, ttl int) error {
	records := make([]record, len(values))
	for i, v := range values {
		records[i] = record{Value: v}
	}
	var out struct {
		Action action `json:"action"`
	}
	code, err := h.call(ctx, http.MethodPost, h.rrset(name, typ)+"/actions/set_records", map[string]any{"records": records}, &out)
	if code == http.StatusNotFound {
		_, err = h.call(ctx, http.MethodPost, "/zones/"+url.PathEscape(h.Zone)+"/rrsets",
			map[string]any{"name": name, "type": typ, "ttl": ttl, "records": records, "labels": map[string]string{"cell": cell}}, &out)
	}
	if err != nil {
		return err
	}
	return h.wait(ctx, out.Action)
}

// Owned lists the record sets created for the cell.
func (h *Hetzner) Owned(ctx context.Context, cell, typ string) ([]string, error) {
	var names []string
	for page := 1; page > 0; {
		var out struct {
			RRSets []struct {
				Name string `json:"name"`
			} `json:"rrsets"`
			Meta struct {
				Pagination struct {
					NextPage *int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		q := url.Values{"type": {typ}, "label_selector": {"cell=" + cell}, "per_page": {"50"}, "page": {strconv.Itoa(page)}}
		if _, err := h.call(ctx, http.MethodGet, "/zones/"+url.PathEscape(h.Zone)+"/rrsets?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, r := range out.RRSets {
			names = append(names, r.Name)
		}
		page = 0
		if n := out.Meta.Pagination.NextPage; n != nil {
			page = *n
		}
	}
	return names, nil
}

// wait returns once the action succeeded, its error if it failed.
func (h *Hetzner) wait(ctx context.Context, a action) error {
	poll := h.Poll
	if poll == 0 {
		poll = time.Second
	}
	for a.Status == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		var out struct {
			Action action `json:"action"`
		}
		if _, err := h.call(ctx, http.MethodGet, "/actions/"+strconv.FormatInt(a.ID, 10), nil, &out); err != nil {
			return err
		}
		a = out.Action
	}
	if a.Status == "error" {
		if a.Error != nil {
			return fmt.Errorf("DNS change %d failed: %s: %s", a.ID, a.Error.Code, a.Error.Message)
		}
		return fmt.Errorf("DNS change %d failed", a.ID)
	}
	if a.Status != "success" {
		return fmt.Errorf("DNS change %d: unexpected status %q", a.ID, a.Status)
	}
	return nil
}

func (h *Hetzner) rrset(name, typ string) string {
	return "/zones/" + url.PathEscape(h.Zone) + "/rrsets/" + url.PathEscape(name) + "/" + typ
}

// call returns the status code also with the error, so callers can tell a
// missing record set from a failure.
func (h *Hetzner) call(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	base := h.BaseURL
	if base == "" {
		base = "https://api.hetzner.cloud/v1"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	client := h.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}
