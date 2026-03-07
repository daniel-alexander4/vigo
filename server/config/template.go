package config

import (
	"bytes"
	"fmt"
	"text/template"
)

// templateContext is the context passed to Go templates when rendering
// resource attributes, module vars, etc.
type templateContext struct {
	Traits map[string]any
	Vars   map[string]any
}

// RenderTemplate renders a Go template string with .Traits and .Vars namespaces.
// Templates are rendered at check-in time (not at config load time) so that
// traits from the requesting agent are available.
func RenderTemplate(tmpl string, traits map[string]any, vars map[string]any) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var buf bytes.Buffer
	ctx := templateContext{
		Traits: traits,
		Vars:   vars,
	}
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return buf.String(), nil
}

// RenderVars renders Go template syntax in var values in declaration order.
// Only string values are rendered; non-string values (int, bool, map, etc.)
// pass through unchanged. Vars are rendered top-down per varOrder so that
// later vars can reference earlier rendered vars via .Vars. Traits are
// available as .Traits. If varOrder is nil, all vars are still rendered but
// in non-deterministic map order. Call this BEFORE resource attribute
// rendering so that rendered vars are available to resource templates.
func RenderVars(vars map[string]any, facts map[string]any, varOrder []string) (map[string]any, error) {
	result := make(map[string]any, len(vars))
	for k, v := range vars {
		result[k] = v
	}

	// Use varOrder for deterministic top-down rendering. Fall back to
	// map iteration if no order is provided.
	keys := varOrder
	if keys == nil {
		keys = make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
	}

	for _, k := range keys {
		v, ok := result[k]
		if !ok {
			continue
		}
		strVal, ok := v.(string)
		if !ok {
			continue
		}
		rendered, err := RenderTemplate(strVal, facts, result)
		if err != nil {
			return nil, fmt.Errorf("rendering var %q: %w", k, err)
		}
		result[k] = rendered
	}
	return result, nil
}
