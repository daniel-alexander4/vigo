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
