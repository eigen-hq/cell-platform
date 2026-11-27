package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	apidoc "github.com/eigen-hq/cell-platform/api"
	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// agentOps maps "METHOD /path" of every operation api/openapi.yaml marks
// x-agent to its operationId: the agents' tools (E-96), the only calls an
// agent makes in a task (E-97).
var agentOps = sync.OnceValues(func() (map[string]string, error) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := yaml.Unmarshal(apidoc.OpenAPI, &doc); err != nil {
		return nil, fmt.Errorf("openapi.yaml: %w", err)
	}
	ops := map[string]string{}
	for path, item := range doc.Paths {
		for method, raw := range item {
			var op struct {
				ID    string `json:"operationId"`
				Agent bool   `json:"x-agent"`
			}
			if method == "parameters" || json.Unmarshal(raw, &op) != nil || !op.Agent {
				continue
			}
			ops[strings.ToUpper(method)+" "+path] = op.ID
		}
	}
	return ops, nil
})

type agentKey struct{}

// agentOf is the Agent of the request's agent caller (nil for people).
func agentOf(ctx context.Context) *cellv1.Agent {
	a, _ := ctx.Value(agentKey{}).(*cellv1.Agent)
	return a
}

// withAgent finds the Agent behind an agent's account,
// cell-agent-<name>.<uid> (E-97). An account in the agents' group without
// one may do nothing, nor may the account of an earlier Agent of the name:
// its tokens may still be cached after the account went.
func (s *Server) withAgent(ctx context.Context, c *Caller) (context.Context, error) {
	name, uid, ok := cell.AgentOf(c.Username)
	if !ok {
		return nil, errorf(http.StatusForbidden, "%s is in group %s but no agent of the cell", c.Username, GroupAgents)
	}
	var a cellv1.Agent
	if err := s.Kube.Get(ctx, client.ObjectKey{Name: name}, &a); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errorf(http.StatusForbidden, "%s is in group %s but there is no agent %s", c.Username, GroupAgents, name)
		}
		return nil, err
	}
	if string(a.UID) != uid {
		return nil, errorf(http.StatusForbidden, "%s is the account of an earlier agent %s", c.Username, name)
	}
	if !a.DeletionTimestamp.IsZero() {
		return nil, errorf(http.StatusForbidden, "agent %s is being deleted", name)
	}
	return context.WithValue(ctx, agentKey{}, &a), nil
}

// mayTake refuses a task the agent does not take: another kind, or an app
// that is not its own.
func mayTake(a *cellv1.Agent, kind, app string) error {
	if a.Spec.Takes(kind, app) {
		return nil
	}
	about := "the whole cell"
	if app != "" {
		about = "app " + app
	}
	return errorf(http.StatusForbidden, "agent %s takes no %s task about %s", a.Name, kind, about)
}

// mayCall refuses an operation that is not one of the agent's tools.
func mayCall(a *cellv1.Agent, method, path string) error {
	ops, err := agentOps()
	if err != nil {
		return err
	}
	if id := ops[method+" "+path]; !a.Spec.MayCall(id) {
		return errorf(http.StatusForbidden, "%s %s is not a tool of agent %s", method, path, a.Name)
	}
	return nil
}

type agentView struct {
	Name         string   `json:"name"`
	Kinds        []string `json:"kinds"`
	Apps         []string `json:"apps"`
	Tools        []string `json:"tools"`
	Instructions string   `json:"instructions"`
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request, c *Caller) error {
	out := struct {
		*Caller
		Agent *agentView `json:"agent,omitempty"`
	}{Caller: c}
	if a := agentOf(r.Context()); a != nil {
		out.Agent = &agentView{Name: a.Name, Kinds: a.Spec.Kinds, Apps: a.Spec.Apps, Tools: a.Spec.Tools, Instructions: a.Spec.Instructions}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
