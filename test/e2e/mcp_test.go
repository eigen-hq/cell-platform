//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// An MCP client sees the tools of two installed apps through the one
// endpoint of the cell's gateway. An account without access to an app sees
// none of its tools and cannot call them. The apps are two Memos from the
// catalog, one for everyone, one for a group.
func TestMCPGateway(t *testing.T) {
	tc := newTestCell(t)
	admin := tc.adminAPI(t)
	member := admin.account(t, "e2e-mcp-member", "e2e-Mcp-member-1", "e2e-mcp-team")
	outsider := admin.account(t, "e2e-mcp-outsider", "e2e-Mcp-outsider-1")
	admin.installedAs(t, "notes", map[string]any{"catalog": "memos", "name": "notes", "subdomain": "notes"}, 10*time.Minute)
	admin.installedAs(t, "journal", map[string]any{"catalog": "memos", "name": "journal", "subdomain": "journal",
		"groups": []string{"e2e-mcp-team"}}, 10*time.Minute)

	memberToken, outsiderToken := member.login(t, "cell-mcp"), outsider.login(t, "cell-mcp")
	const list, call = "notes_memo_list_memos", "journal_memo_list_memos"
	// The operator writes the gateway's configuration; the kubelet takes
	// about a minute to hand it to the gateway.
	waitFor(t, 3*time.Minute, func() error {
		code, msg := tc.mcp(t, memberToken, "tools/list", nil)
		if names := toolNames(msg); code != http.StatusOK || !slices.Contains(names, list) || !slices.Contains(names, call) {
			return fmt.Errorf("member sees HTTP %d %v", code, names)
		}
		return nil
	})
	_, msg := tc.mcp(t, memberToken, "tools/list", nil)
	t.Logf("through https://%s/mcp a member of e2e-mcp-team sees %d tools of notes and journal, e.g. %s", tc.cfg.Host("mcp"), len(toolNames(msg)), call)
	code, msg := tc.mcp(t, outsiderToken, "tools/list", nil)
	names := toolNames(msg)
	if code != http.StatusOK || !slices.Contains(names, list) {
		t.Fatalf("outsider: HTTP %d %v", code, msg)
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "notes_") {
			t.Errorf("outsider sees %s", n)
		}
	}
	t.Logf("an account outside the group sees %d tools, all of notes", len(names))

	// A call to an app the account may not use does not reach the app.
	code, msg = tc.mcp(t, outsiderToken, "tools/call", map[string]any{"name": call, "arguments": map[string]any{}})
	if msg["error"] == nil || msg["result"] != nil {
		t.Errorf("outsider calls %s: HTTP %d %v", call, code, msg)
	}
	// The member's call reaches Memos. Memos wants a token of its own,
	// which the gateway does not have (O-18), and says so as the tool's
	// answer; the user's token for the gateway does not reach the app.
	code, msg = tc.mcp(t, memberToken, "tools/call", map[string]any{"name": call, "arguments": map[string]any{}})
	text, _ := json.Marshal(field(msg, "result", "content"))
	if code != http.StatusOK || field(msg, "result", "isError") != true || !strings.Contains(string(text), "authentication required") {
		t.Errorf("member calls %s: HTTP %d %v", call, code, msg)
	}

	// Only tokens for the gateway count: none, or one for the API, is
	// refused, and a token for the gateway does not work at the API.
	for _, token := range []string{"", member.bearer(t)} {
		if code, _ := tc.mcp(t, token, "tools/list", nil); code != http.StatusUnauthorized {
			t.Errorf("tools/list with token %.10q: HTTP %d, want 401", token, code)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://"+tc.cfg.Host("api")+"/v0/apps", nil)
	req.Header.Set("Authorization", "Bearer "+memberToken)
	resp, err := tc.akHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("API with a gateway token: HTTP %d %.200s", resp.StatusCode, body)
	}
}

// mcp sends one JSON-RPC request to the cell's MCP gateway and returns the
// HTTP status and the message (from JSON or the first server-sent event);
// the message is nil if the answer is none.
func (tc *testCell) mcp(t *testing.T, token, method string, params any) (int, map[string]any) {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, "https://"+tc.cfg.Host("mcp")+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := tc.akHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data := readBody(t, resp)
	for _, line := range strings.Split(data, "\n") {
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data = rest
			break
		}
	}
	var msg map[string]any
	if json.Unmarshal([]byte(data), &msg) != nil {
		msg = nil
	}
	return resp.StatusCode, msg
}

func toolNames(msg map[string]any) []string {
	var names []string
	tools, _ := field(msg, "result", "tools").([]any)
	for _, tool := range tools {
		if n, ok := field(tool, "name").(string); ok {
			names = append(names, n)
		}
	}
	return names
}
