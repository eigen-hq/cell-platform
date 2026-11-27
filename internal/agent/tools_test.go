package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The agents' tools are the operations api/openapi.yaml marks x-agent. A
// new one is a decision (E-96), not a side effect of the contract.
func TestOperations(t *testing.T) {
	ops, err := operations()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, op := range ops {
		names = append(names, op.Name)
		params, _ := json.Marshal(op.Params)
		if op.Description == "" || strings.Contains(string(params), "$ref") || strings.Contains(string(params), "nullable") {
			t.Errorf("%s: description %q, params %s", op.Name, op.Description, params)
		}
	}
	want := []string{"appEvents", "appLogs", "appUsage", "getApp", "getCatalogEntry", "getChange", "installApp", "listApps",
		"listBackups", "listCatalog", "listChanges", "listNodes", "listPods", "nodeUsage", "removeApp", "restartApp", "restoreApp", "updateApp"}
	if !slices.Equal(names, want) {
		t.Errorf("tools %q, want %q", names, want)
	}
}

func TestRequest(t *testing.T) {
	ops, err := operations()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ op, args, path, body, err string }{
		{op: "appLogs", args: `{"name":"a b","tail":50,"previous":true}`, path: "/v0/apps/a%20b/logs?previous=true&tail=50"},
		{op: "updateApp", args: `{"name":"notes","patch":{"version":"2"},"reason":"r"}`, path: "/v0/apps/notes", body: `{"patch":{"version":"2"},"reason":"r"}`},
		{op: "restartApp", args: `{"name":"notes"}`, path: "/v0/apps/notes/restart", body: `{}`},
		{op: "removeApp", args: `{"name":"notes","reason":"asked"}`, path: "/v0/apps/notes?reason=asked"},
		{op: "listApps", path: "/v0/apps"},
		{op: "listChanges", args: `{"app":null}`, path: "/v0/changes"},
		{op: "getApp", args: `{}`, err: `argument "name": a name is required`},
		{op: "getApp", args: `{"name":""}`, err: `argument "name": a name is required`},
		{op: "getApp", args: `{"name":3}`, err: `argument "name": a name is required`},
		{op: "getApp", args: `{"name":".."}`, err: `argument "name": a name is required`},
		{op: "getApp", args: `{"name":"."}`, err: `argument "name": a name is required`},
		{op: "getApp", args: `{"name":"x","app":"y"}`, err: `unknown argument "app"; getApp takes name`},
	} {
		i := slices.IndexFunc(ops, func(o operation) bool { return o.Name == c.op })
		if i < 0 {
			t.Fatalf("no tool %s", c.op)
		}
		path, body, err := ops[i].request(json.RawMessage(c.args))
		got := ""
		if body != nil {
			b, _ := json.Marshal(body)
			got = string(b)
		}
		if path != c.path || got != c.body || (err == nil) != (c.err == "") || (err != nil && err.Error() != c.err) {
			t.Errorf("%s %s: %q %s %v, want %q %s %q", c.op, c.args, path, got, err, c.path, c.body, c.err)
		}
	}
}
