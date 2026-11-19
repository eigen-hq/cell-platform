package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// render executes every string inside v as a Go template over data and
// returns the result as a new value of the same type. Placeholders that do
// not exist (e.g. {{ .database.host }} without a database) are an error.
func render[T any](v T, data map[string]any) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return out, err
	}
	if tree, err = renderTree(tree, data); err != nil {
		return out, err
	}
	if raw, err = json.Marshal(tree); err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func renderTree(v any, data map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		if !strings.Contains(t, "{{") {
			return t, nil
		}
		tpl, err := template.New("").Option("missingkey=error").Parse(t)
		if err != nil {
			return nil, fmt.Errorf("placeholder %q: %w", t, err)
		}
		var b strings.Builder
		if err := tpl.Execute(&b, data); err != nil {
			return nil, fmt.Errorf("placeholder %q: %w", t, err)
		}
		return b.String(), nil
	case map[string]any:
		for k, x := range t {
			r, err := renderTree(x, data)
			if err != nil {
				return nil, err
			}
			t[k] = r
		}
	case []any:
		for i, x := range t {
			r, err := renderTree(x, data)
			if err != nil {
				return nil, err
			}
			t[i] = r
		}
	}
	return v, nil
}
