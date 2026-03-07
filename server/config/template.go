package config

import (
	"bytes"
	"fmt"
	"text/template"
)

// templateContext is the context passed to Go templates when rendering
// resource attributes, module vars, etc.
type templateContext struct {
	Facts map[string]any
	Vars  map[string]any
}

// RenderTemplate renders a Go template string with .Facts and .Vars namespaces.
// Templates are rendered at check-in time (not at config load time) so that
// facts from the requesting agent are available.
func RenderTemplate(tmpl string, facts map[string]any, vars map[string]any) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var buf bytes.Buffer
	ctx := templateContext{
		Facts: facts,
		Vars:  vars,
	}
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return buf.String(), nil
}

// RenderVars renders Go template syntax in var values. Only string values
// are rendered; non-string values (int, bool, map, etc.) pass through unchanged.
// Facts are available as .Facts, and other vars (in their current render-pass state)
// are available as .Vars. This is a single-pass render: a var referencing another
// var will see that var's pre-rendered value. Call this BEFORE resource attribute
// rendering so that rendered vars are available to resource templates.
func RenderVars(vars map[string]any, facts map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(vars))
	for k, v := range vars {
		strVal, ok := v.(string)
		if !ok {
			result[k] = v
			continue
		}
		rendered, err := RenderTemplate(strVal, facts, vars)
		if err != nil {
			return nil, fmt.Errorf("rendering var %q: %w", k, err)
		}
		result[k] = rendered
	}
	return result, nil
}
