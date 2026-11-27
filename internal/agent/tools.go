package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// maxResult caps what one tool call hands the model.
const maxResult = 30000

// tool is a function the model may call.
type tool struct {
	Name, Description string
	// Params is the JSON schema of the arguments.
	Params map[string]any
	Run    func(ctx context.Context, args json.RawMessage) (string, error)
}

type none struct{}

type appArg struct {
	App string `json:"app"`
}

type logsArg struct {
	App       string `json:"app"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Previous  bool   `json:"previous"`
	Tail      int    `json:"tail"`
}

type catalogArg struct {
	Name string `json:"name"`
}

type updateArg struct {
	App    string         `json:"app"`
	Patch  map[string]any `json:"patch"`
	Reason string         `json:"reason"`
}

type installArg struct {
	App      string         `json:"app"`
	Manifest map[string]any `json:"manifest"`
	Reason   string         `json:"reason"`
}

// object is the schema of an object; the first props are required.
func object(required int, props ...prop) map[string]any {
	p, req := map[string]any{}, []string{}
	for i, pr := range props {
		p[pr.name] = map[string]any{"type": pr.typ, "description": pr.desc}
		if i < required {
			req = append(req, pr.name)
		}
	}
	s := map[string]any{"type": "object", "properties": p, "additionalProperties": false}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}

type prop struct{ name, typ, desc string }

var appProp = prop{"app", "string", "Name of the app"}

// newTool decodes the arguments into T and runs fn.
func newTool[T any](name, desc string, params map[string]any, fn func(context.Context, T) (string, error)) tool {
	return tool{Name: name, Description: desc, Params: params, Run: func(ctx context.Context, args json.RawMessage) (string, error) {
		var in T
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("arguments: %w", err)
		}
		return fn(ctx, in)
	}}
}

// tools are the agent's tools in task: each is one call of the platform
// API, which records it as a step of the task.
func (a *Agent) tools(task string) []tool {
	call := func(ctx context.Context, method, path string, in any) (string, error) {
		code, body, err := a.API.Do(ctx, method, path, task, in)
		if err != nil {
			return "", err
		}
		text := string(body)
		if len(text) > maxResult {
			text = text[len(text)-maxResult:] // the end of logs and lists matters most
		}
		if code >= 300 {
			return "", fmt.Errorf("HTTP %d: %s", code, text)
		}
		return text, nil
	}
	get := func(path string) func(context.Context, none) (string, error) {
		return func(ctx context.Context, _ none) (string, error) { return call(ctx, http.MethodGet, path, nil) }
	}
	app := func(name, desc, suffix string) tool {
		return newTool(name, desc, object(1, appProp), func(ctx context.Context, in appArg) (string, error) {
			return call(ctx, http.MethodGet, "/v0/apps/"+url.PathEscape(in.App)+suffix, nil)
		})
	}
	return []tool{
		newTool("list_apps", "List the cell's apps with phase and conditions.", object(0), get("/v0/apps")),
		app("get_app", "An app: phase, conditions with the operator's messages, and its spec (the manifest).", ""),
		app("get_pods", "The app's pods with container states, restarts, last exit, and per container the CPU and memory in use next to its requests and limits.", "/pods"),
		app("get_backups", "The app's backups: when they ran and whether they succeeded.", "/backups"),
		newTool("list_nodes", "The cell's machines: role, readiness, CPU, memory and disk.", object(0), get("/v0/nodes")),
		app("get_events", "Recent Kubernetes events in the app's namespace (scheduling, probes, image pulls).", "/events"),
		newTool("get_logs", "Logs of a container of the app.", object(1, appProp,
			prop{"pod", "string", "Pod name (default: the newest pod of the app)"},
			prop{"container", "string", "Container name (default: the pod's first container)"},
			prop{"previous", "boolean", "Logs of the container's previous run (after a crash)"},
			prop{"tail", "integer", "Lines from the end (default 200)"}),
			func(ctx context.Context, in logsArg) (string, error) {
				q := url.Values{}
				for k, v := range map[string]string{"pod": in.Pod, "container": in.Container} {
					if v != "" {
						q.Set(k, v)
					}
				}
				if in.Previous {
					q.Set("previous", "true")
				}
				if in.Tail > 0 {
					q.Set("tail", strconv.Itoa(in.Tail))
				}
				path := "/v0/apps/" + url.PathEscape(in.App) + "/logs"
				if len(q) > 0 {
					path += "?" + q.Encode()
				}
				return call(ctx, http.MethodGet, path, nil)
			}),
		newTool("list_changes", "The app's change log: proposed and finished changes with their outcome.", object(1, appProp),
			func(ctx context.Context, in appArg) (string, error) {
				return call(ctx, http.MethodGet, "/v0/changes?app="+url.QueryEscape(in.App), nil)
			}),
		newTool("list_catalog", "The catalog of maintained app manifests.", object(0), get("/v0/catalog")),
		newTool("get_catalog_entry", "A catalog entry with its full manifest, as an example of a working manifest.",
			object(1, prop{"name", "string", "Name of the catalog entry"}),
			func(ctx context.Context, in catalogArg) (string, error) {
				return call(ctx, http.MethodGet, "/v0/catalog/"+url.PathEscape(in.Name), nil)
			}),
		newTool("propose_update",
			"Propose a change to an app's spec. Nothing changes until an admin approves it; the platform backs the app up first and rolls back if the app is not healthy afterwards.",
			object(3, appProp,
				prop{"patch", "object", "JSON merge patch on the app spec; null removes a field and a list replaces the whole list"},
				prop{"reason", "string", "The cause you found with its evidence and what the patch changes"}),
			func(ctx context.Context, in updateArg) (string, error) {
				return call(ctx, http.MethodPatch, "/v0/apps/"+url.PathEscape(in.App), map[string]any{"patch": in.Patch, "reason": in.Reason})
			}),
		newTool("propose_install",
			"Propose installing a new app from a manifest. The API checks the manifest first and answers with the errors it finds; nothing is installed until an admin approves.",
			object(3, prop{"app", "string", "Name of the new app"},
				prop{"manifest", "object", "The app spec: the spec part of an App manifest"},
				prop{"reason", "string", "Where the manifest comes from and what an approver should check"}),
			func(ctx context.Context, in installArg) (string, error) {
				return call(ctx, http.MethodPost, "/v0/apps", map[string]any{"name": in.App, "manifest": in.Manifest, "reason": in.Reason})
			}),
	}
}
