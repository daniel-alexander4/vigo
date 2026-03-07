// Package runner provides the convergence runner and supporting utilities.
package runner

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// whenContext provides the template execution context with Traits accessible
// via .Traits in template expressions.
type whenContext struct {
	Traits map[string]any
}

// EvaluateWhen evaluates a Go template expression against a facts map.
// An empty expression returns true (no condition means always run).
// The result is trimmed of whitespace; only the exact string "true" evaluates
// to true. Any other output (empty, "false", "<no value>") evaluates to false.
// Returns an error on template parse or execution failure.
func EvaluateWhen(expr string, facts map[string]any) (bool, error) {
	if strings.TrimSpace(expr) == "" {
		return true, nil
	}

	tmpl, err := template.New("when").Option("missingkey=zero").Parse(expr)
	if err != nil {
		return false, fmt.Errorf("parse when expression: %w", err)
	}

	ctx := whenContext{Traits: facts}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return false, fmt.Errorf("execute when expression: %w", err)
	}

	result := strings.TrimSpace(buf.String())
	return result == "true", nil
}
