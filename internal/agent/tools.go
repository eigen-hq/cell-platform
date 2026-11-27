package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"

	apidoc "github.com/eigen-hq/cell-platform/api"
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

// operation is an operation of the platform API that api/openapi.yaml
// marks x-agent: the contract is the one source of the agents' tools.
type operation struct {
	Name, Description, Method, Path string
	// Params is the JSON schema of the arguments: the operation's path and
	// query parameters and the properties of its JSON body, side by side.
	Params map[string]any
	in     map[string]string // where an argument goes: path, query or body
	body   bool              // the operation takes a JSON body
}

// operations are the agents' tools in the contract, sorted by name so the
// start of every prompt stays the same for the gateway's cache.
var operations = sync.OnceValues(func() ([]operation, error) { return parseOperations(apidoc.OpenAPI) })

func parseOperations(spec []byte) ([]operation, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("openapi.yaml: %w", err)
	}
	paths, _ := doc["paths"].(map[string]any)
	var ops []operation
	for path, item := range paths {
		item, _ := item.(map[string]any)
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			o, ok := item[method].(map[string]any)
			if !ok || o["x-agent"] != true {
				continue
			}
			op, err := parseOperation(doc, strings.ToUpper(method), path, item, o)
			if err != nil {
				return nil, fmt.Errorf("openapi.yaml: %s %s: %w", method, path, err)
			}
			ops = append(ops, op)
		}
	}
	slices.SortFunc(ops, func(a, b operation) int { return strings.Compare(a.Name, b.Name) })
	return ops, nil
}

func parseOperation(doc map[string]any, method, path string, item, o map[string]any) (operation, error) {
	op := operation{Method: method, Path: path, in: map[string]string{}}
	op.Name, _ = o["operationId"].(string)
	op.Description, _ = o["description"].(string)
	if op.Description = strings.TrimSpace(op.Description); op.Name == "" || op.Description == "" {
		return op, errors.New("a tool needs an operationId and a description")
	}
	props, required := map[string]any{}, []string{}
	add := func(name, in string, schema any, req bool) error {
		if _, dup := op.in[name]; dup || name == "" {
			return fmt.Errorf("argument %q is empty or twice", name)
		}
		op.in[name], props[name] = in, schema
		if req {
			required = append(required, name)
		}
		return nil
	}
	params, _ := item["parameters"].([]any)
	more, _ := o["parameters"].([]any)
	for _, p := range append(slices.Clone(params), more...) {
		p, err := inline(doc, p, 0)
		if err != nil {
			return op, err
		}
		pm, _ := p.(map[string]any)
		name, _ := pm["name"].(string)
		in, _ := pm["in"].(string)
		if in != "path" && in != "query" {
			return op, fmt.Errorf("parameter %q in %q: only path and query become arguments", name, in)
		}
		schema, _ := pm["schema"].(map[string]any)
		if schema == nil {
			schema = map[string]any{}
		}
		if d, ok := pm["description"].(string); ok {
			schema["description"] = d
		}
		req, _ := pm["required"].(bool)
		if err := add(name, in, schema, in == "path" || req); err != nil {
			return op, err
		}
	}
	if rb, ok := o["requestBody"]; ok {
		rb, err := inline(doc, rb, 0)
		if err != nil {
			return op, err
		}
		rbm, _ := rb.(map[string]any)
		content, _ := rbm["content"].(map[string]any)
		js, _ := content["application/json"].(map[string]any)
		schema, _ := js["schema"].(map[string]any)
		if schema == nil {
			return op, errors.New("a request body other than JSON")
		}
		op.body = true
		bodyProps, _ := schema["properties"].(map[string]any)
		bodyReq, _ := schema["required"].([]any)
		for name, s := range bodyProps {
			if err := add(name, "body", s, slices.Contains(bodyReq, any(name))); err != nil {
				return op, err
			}
		}
	}
	slices.Sort(required)
	op.Params = map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		op.Params["required"] = required
	}
	return op, nil
}

// inline resolves every $ref against doc and drops what JSON Schema does
// not know (nullable, x- extensions): the model gets one schema per tool.
func inline(doc map[string]any, v any, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("schema nested too deep (a $ref cycle?)")
	}
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			if !strings.HasPrefix(ref, "#/") {
				return nil, fmt.Errorf("$ref %s: only references inside the document", ref)
			}
			var target any = doc
			for key := range strings.SplitSeq(ref[2:], "/") {
				m, _ := target.(map[string]any)
				if target = m[key]; target == nil {
					return nil, fmt.Errorf("$ref %s not found", ref)
				}
			}
			return inline(doc, target, depth+1)
		}
		out := make(map[string]any, len(v))
		for k, x := range v {
			if k == "nullable" || strings.HasPrefix(k, "x-") {
				continue
			}
			x, err := inline(doc, x, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = x
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			x, err := inline(doc, x, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	}
	return v, nil
}

// request turns the arguments of a call into the API's path and body.
func (op operation) request(args json.RawMessage) (string, any, error) {
	var in map[string]json.RawMessage
	if len(bytes.TrimSpace(args)) > 0 { // some models send no arguments at all
		if err := json.Unmarshal(args, &in); err != nil {
			return "", nil, fmt.Errorf("arguments: %w", err)
		}
	}
	path, query, body := op.Path, url.Values{}, map[string]json.RawMessage{}
	for name, raw := range in {
		switch op.in[name] {
		case "body":
			body[name] = raw
		case "query":
			if string(raw) == "null" {
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) != nil {
				s = string(raw) // a number or a boolean as the model wrote it
			}
			query.Set(name, s)
		case "path":
		default:
			return "", nil, fmt.Errorf("unknown argument %q; %s takes %s", name, op.Name, strings.Join(slices.Sorted(maps.Keys(op.in)), ", "))
		}
	}
	for name, where := range op.in {
		if where != "path" {
			continue
		}
		// An empty name would call another operation (/v0/apps/ lists
		// apps), and so would . or .., which the router cleans away.
		var s string
		if err := json.Unmarshal(in[name], &s); err != nil || s == "" || s == "." || s == ".." {
			return "", nil, fmt.Errorf("argument %q: a name is required", name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(s))
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	if !op.body {
		return path, nil, nil
	}
	return path, body, nil
}

// tools are the agent's tools in task, the ones its Agent names (* for
// all, E-97): each is one call of the platform API, which records it as a
// step of the task and decides whether the agent may make it. ask comes
// last, for every agent.
func (a *Agent) tools(task string, names []string) ([]tool, error) {
	ops, err := operations()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if n != "*" && !slices.ContainsFunc(ops, func(op operation) bool { return op.Name == n }) {
			return nil, fmt.Errorf("the agent's Agent names %s, which is no tool of the API", n)
		}
	}
	var tools []tool
	for _, op := range ops {
		if !slices.Contains(names, "*") && !slices.Contains(names, op.Name) {
			continue
		}
		tools = append(tools, tool{Name: op.Name, Description: op.Description, Params: op.Params,
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				path, body, err := op.request(args)
				if err != nil {
					return "", err
				}
				return a.call(ctx, op.Method, path, task, body)
			}})
	}
	return append(tools, ask), nil
}

// call makes one API call in task and returns its body for the model.
func (a *Agent) call(ctx context.Context, method, path, task string, in any) (string, error) {
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
