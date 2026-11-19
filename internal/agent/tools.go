package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/toolrunner"
)

// maxResult caps what one tool call hands the model.
const maxResult = 30000

type none struct{}

type appArg struct {
	App string `json:"app" jsonschema:"required,description=Name of the app"`
}

type logsArg struct {
	App       string `json:"app" jsonschema:"required,description=Name of the app"`
	Pod       string `json:"pod,omitempty" jsonschema:"description=Pod name (default: the newest pod of the app)"`
	Container string `json:"container,omitempty" jsonschema:"description=Container name (default: the pod's first container)"`
	Previous  bool   `json:"previous,omitempty" jsonschema:"description=Logs of the container's previous run (after a crash)"`
	Tail      int    `json:"tail,omitempty" jsonschema:"description=Lines from the end (default 200)"`
}

type catalogArg struct {
	Name string `json:"name" jsonschema:"required,description=Name of the catalog entry"`
}

type updateArg struct {
	App    string         `json:"app" jsonschema:"required,description=Name of the app"`
	Patch  map[string]any `json:"patch" jsonschema:"required,description=JSON merge patch on the app spec; null removes a field and a list replaces the whole list"`
	Reason string         `json:"reason" jsonschema:"required,description=The cause you found with its evidence and what the patch changes"`
}

type installArg struct {
	App      string         `json:"app" jsonschema:"required,description=Name of the new app"`
	Manifest map[string]any `json:"manifest" jsonschema:"required,description=The app spec: the spec part of an App manifest"`
	Reason   string         `json:"reason" jsonschema:"required,description=Where the manifest comes from and what an approver should check"`
}

// tools are the agent's tools in task: each is one call of the platform
// API, which records it as a step of the task.
func (a *Agent) tools(task string) ([]anthropic.BetaTool, error) {
	call := func(ctx context.Context, method, path string, in any) (anthropic.BetaToolResultBlockParamContentUnion, error) {
		code, body, err := a.API.Do(ctx, method, path, task, in)
		if err != nil {
			return anthropic.BetaToolResultBlockParamContentUnion{}, err
		}
		text := string(body)
		if len(text) > maxResult {
			text = text[len(text)-maxResult:] // the end of logs and lists matters most
		}
		if code >= 300 {
			return anthropic.BetaToolResultBlockParamContentUnion{}, fmt.Errorf("HTTP %d: %s", code, text)
		}
		return anthropic.BetaToolResultBlockParamContentUnion{OfText: &anthropic.BetaTextBlockParam{Text: text}}, nil
	}
	app := func(name, desc, suffix string) (anthropic.BetaTool, error) {
		return toolrunner.NewBetaToolFromJSONSchema(name, desc, func(ctx context.Context, in appArg) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			return call(ctx, http.MethodGet, "/v0/apps/"+url.PathEscape(in.App)+suffix, nil)
		})
	}
	var tools []anthropic.BetaTool
	for _, mk := range []func() (anthropic.BetaTool, error){
		func() (anthropic.BetaTool, error) {
			return toolrunner.NewBetaToolFromJSONSchema("list_apps", "List the cell's apps with phase and conditions.",
				func(ctx context.Context, _ none) (anthropic.BetaToolResultBlockParamContentUnion, error) {
					return call(ctx, http.MethodGet, "/v0/apps", nil)
				})
		},
		func() (anthropic.BetaTool, error) {
			return app("get_app", "An app: phase, conditions with the operator's messages, and its spec (the manifest).", "")
		},
		func() (anthropic.BetaTool, error) {
			return app("get_pods", "The app's pods with container states, restarts and last exit.", "/pods")
		},
		func() (anthropic.BetaTool, error) {
			return app("get_events", "Recent Kubernetes events in the app's namespace (scheduling, probes, image pulls).", "/events")
		},
		func() (anthropic.BetaTool, error) {
			return toolrunner.NewBetaToolFromJSONSchema("get_logs", "Logs of a container of the app.",
				func(ctx context.Context, in logsArg) (anthropic.BetaToolResultBlockParamContentUnion, error) {
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
				})
		},
		func() (anthropic.BetaTool, error) {
			return toolrunner.NewBetaToolFromJSONSchema("list_changes", "The app's change log: proposed and finished changes with their outcome.",
				func(ctx context.Context, in appArg) (anthropic.BetaToolResultBlockParamContentUnion, error) {
					return call(ctx, http.MethodGet, "/v0/changes?app="+url.QueryEscape(in.App), nil)
				})
		},
		func() (anthropic.BetaTool, error) {
			return toolrunner.NewBetaToolFromJSONSchema("list_catalog", "The catalog of maintained app manifests.",
				func(ctx context.Context, _ none) (anthropic.BetaToolResultBlockParamContentUnion, error) {
					return call(ctx, http.MethodGet, "/v0/catalog", nil)
				})
		},
		func() (anthropic.BetaTool, error) {
			return toolrunner.NewBetaToolFromJSONSchema("get_catalog_entry", "A catalog entry with its full manifest, as an example of a working manifest.",
				func(ctx context.Context, in catalogArg) (anthropic.BetaToolResultBlockParamContentUnion, error) {
					return call(ctx, http.MethodGet, "/v0/catalog/"+url.PathEscape(in.Name), nil)
				})
		},
		func() (anthropic.BetaTool, error) {
			return toolrunner.NewBetaToolFromJSONSchema("propose_update",
				"Propose a change to an app's spec. Nothing changes until an admin approves it; the platform backs the app up first and rolls back if the app is not healthy afterwards.",
				func(ctx context.Context, in updateArg) (anthropic.BetaToolResultBlockParamContentUnion, error) {
					return call(ctx, http.MethodPatch, "/v0/apps/"+url.PathEscape(in.App), map[string]any{"patch": in.Patch, "reason": in.Reason})
				})
		},
		func() (anthropic.BetaTool, error) {
			return toolrunner.NewBetaToolFromJSONSchema("propose_install",
				"Propose installing a new app from a manifest. The API checks the manifest first and answers with the errors it finds; nothing is installed until an admin approves.",
				func(ctx context.Context, in installArg) (anthropic.BetaToolResultBlockParamContentUnion, error) {
					return call(ctx, http.MethodPost, "/v0/apps", map[string]any{"name": in.App, "manifest": in.Manifest, "reason": in.Reason})
				})
		},
	} {
		t, err := mk()
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, nil
}
