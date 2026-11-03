// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"fmt"
	"strings"
	"text/template"
)

// templateSyntaxRule runs Go's text/template parser over every
// resource attribute that may carry a template, so a malformed
// `{{ ... }}` directive fails the publish instead of the agent.
//
// Without this, a missing closing `}}` or unmatched `{{ if }}` /
// `{{ end }}` rides through to the agent, where the runner fails
// the resource at template-render time. Operators see a converged
// module that doesn't actually deploy.
//
// Scope: any resource attribute whose value is a string containing
// the `{{` template-open delimiter. We don't run on every string
// (most are literal values), and we don't try to follow `source:`
// references to external `templates/*.tmpl` files — those are
// linted separately when modlint extends to the templates tree
// (a later phase). Currently `content:` is the dominant carrier
// of inline templates.
//
// Grammar-only by intent: text/template.Parse also validates
// function references, but the agent's runtime funcMap (sprig,
// vigo-specific helpers) is not visible at lint time. Without
// filtering, `{{ default .X "y" }}` and similar would be falsely
// rejected. We check the error message for the "function ... not
// defined" pattern and suppress those — the genuine syntax errors
// (unmatched delimiters, dangling actions, missing `{{ end }}`)
// get reported.
type templateSyntaxRule struct{}

func (templateSyntaxRule) id() string { return "template-syntax" }

// isFuncNotDefinedError matches the error text/template emits when
// the parser sees an unknown function name. Filtering on this string
// is fragile across Go versions but the format has been stable since
// Go 1.0 — the alternative (registering a permissive funcMap that
// accepts every name) requires pre-knowing the names, which we don't.
func isFuncNotDefinedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "function") && strings.Contains(err.Error(), "not defined")
}

func (templateSyntaxRule) apply(mod *configcrate, _ Options) []Finding {
	var out []Finding
	for _, res := range mod.Resources {
		for _, key := range res.AttrOrder {
			s, ok := res.Attrs[key].(string)
			if !ok || !strings.Contains(s, "{{") {
				continue
			}
			// Use the resource name + attribute as the template name
			// for clearer error messages — text/template surfaces this
			// in parse errors.
			tmplName := fmt.Sprintf("%s.%s", res.Name, key)
			_, err := template.New(tmplName).Parse(s)
			if err == nil || isFuncNotDefinedError(err) {
				continue
			}
			// text/template error messages include the template
			// name and a line:col reference relative to the
			// template text, which is good enough for operators
			// to locate the issue inside a multi-line content:
			// block.
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "template-syntax",
				Message: fmt.Sprintf(
					"resource %q attribute %q: template syntax error — %s",
					res.Name, key, err.Error(),
				),
			})
		}
	}
	return out
}

func init() { register(templateSyntaxRule{}) }
