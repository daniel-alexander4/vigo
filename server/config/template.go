// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/daniel-alexander4/vigo/server/conditionals"
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

// ResolveMatchVars evaluates match vars against traits and sets the resolved
// string values in the vars map. Each match var has a list of when/value branches;
// the first branch whose when expression evaluates to true wins. If no branch
// matches, the default value is used (or the var is omitted).
//
// Match vars use the same when expression syntax as resource when: fields.
// Builtin functions (distro, arch, os_family) are evaluated server-side by
// looking up the corresponding trait values directly.
//
// Call this BEFORE RenderVars so that resolved match vars are available as
// regular string vars for template rendering.
func ResolveMatchVars(
	vars map[string]any,
	varOrder *[]string,
	matchVars map[string][]MatchBranch,
	matchVarDefaults map[string]string,
	traits map[string]any,
) {
	if len(matchVars) == 0 {
		return
	}
	for name, branches := range matchVars {
		var resolved string
		matched := false
		for _, b := range branches {
			if evalMatchWhen(b.When, vars, traits) {
				resolved = b.Value
				matched = true
				break
			}
		}
		if !matched {
			if def, ok := matchVarDefaults[name]; ok {
				resolved = def
			} else {
				continue // no match, no default — skip this var
			}
		}
		vars[name] = resolved
		found := false
		for _, k := range *varOrder {
			if k == name {
				found = true
				break
			}
		}
		if !found {
			*varOrder = append(*varOrder, name)
		}
	}
}

// evalMatchWhen evaluates a when expression for match var resolution.
// Supports:
//   - Conditional var references: "is_debian" → looks up in vars
//   - Builtin functions: distro('ubuntu'), arch('amd64'), os_family('debian')
//   - Boolean operators: and, or, not/!
//
// First tries full server-side evaluation via EvaluateResourceWhen.
// If that passes through builtins (returns agentWhen != ""), falls back
// to simple pattern matching against trait values.
func evalMatchWhen(when string, vars map[string]any, traits map[string]any) bool {
	// Try the standard evaluator first (handles var-only expressions).
	include, agentWhen, err := EvaluateResourceWhen(when, vars)
	if err != nil {
		return false
	}
	// If fully resolved server-side, use that result.
	if agentWhen == "" {
		return include
	}

	// Has builtins — evaluate them against traits directly.
	// This is a simplified evaluator for the common builtins.
	return evalBuiltinsServerSide(when, traits)
}

// evalBuiltinsServerSide evaluates when expressions containing builtin
// functions by looking up trait values directly. Handles the common cases
// needed for match vars (distro, arch, os_family, version_ge).
//
// Supports "and" (AND), "or" (OR), and "!" / "not" (NOT). Uses the same
// keyword operators as the main parser and the Rust agent parser.
func evalBuiltinsServerSide(when string, traits map[string]any) bool {
	// Handle "and" / "&&" — all parts must be true.
	// Use word-boundary-aware split to avoid matching "and" inside identifiers.
	if strings.Contains(when, " && ") {
		parts := strings.Split(when, " && ")
		for _, part := range parts {
			if !evalBuiltinsServerSide(strings.TrimSpace(part), traits) {
				return false
			}
		}
		return true
	}
	if containsWord(when, " and ") {
		parts := strings.Split(when, " and ")
		for _, part := range parts {
			if !evalBuiltinsServerSide(strings.TrimSpace(part), traits) {
				return false
			}
		}
		return true
	}

	// Handle "or" / "||" — any part true is enough.
	if strings.Contains(when, " || ") {
		parts := strings.Split(when, " || ")
		for _, part := range parts {
			if evalBuiltinsServerSide(strings.TrimSpace(part), traits) {
				return true
			}
		}
		return false
	}
	if containsWord(when, " or ") {
		parts := strings.Split(when, " or ")
		for _, part := range parts {
			if evalBuiltinsServerSide(strings.TrimSpace(part), traits) {
				return true
			}
		}
		return false
	}

	// Handle "!" or "not " prefix.
	trimmed := strings.TrimSpace(when)
	if strings.HasPrefix(trimmed, "!") {
		return !evalBuiltinsServerSide(strings.TrimPrefix(trimmed, "!"), traits)
	}
	if strings.HasPrefix(trimmed, "not ") {
		return !evalBuiltinsServerSide(strings.TrimPrefix(trimmed, "not "), traits)
	}

	// Extract function call: name('arg')
	if idx := strings.Index(trimmed, "("); idx > 0 {
		funcName := trimmed[:idx]
		argStr := strings.Trim(trimmed[idx:], "()'\"")

		// Traits may be flat ("os.family" keys) or nested ({"os": {"family": ...}}).
		// Try flat keys first (agent check-in format), fall back to nested.
		traitVal := func(key string) string {
			// Flat: "os.family" → traits["os.family"]
			if v, ok := traits[key]; ok {
				if s, ok := v.(string); ok {
					return s
				}
			}
			// Nested: "os.family" → traits["os"]["family"]
			parts := strings.SplitN(key, ".", 2)
			if len(parts) == 2 {
				if sub, ok := traits[parts[0]].(map[string]any); ok {
					if s, ok := sub[parts[1]].(string); ok {
						return s
					}
				}
			}
			return ""
		}

		switch funcName {
		case "distro":
			return strings.EqualFold(traitVal("os.distro"), argStr)
		case "os_family":
			return strings.EqualFold(traitVal("os.family"), argStr)
		case "os":
			return strings.EqualFold(traitVal("os.name"), argStr)
		case "arch":
			return strings.EqualFold(traitVal("os.arch"), argStr)
		case "version_ge":
			return traitVal("os.version") >= argStr
		}
	}

	return false
}

// MatchBranch re-exported from conditionals for use in config package.
type MatchBranch = conditionals.MatchBranch

// RenderVars renders Go template syntax in var values in declaration order.
// Only string values are rendered; non-string values (int, bool, map, etc.)
// pass through unchanged. Vars are rendered top-down per varOrder so that
// later vars can reference earlier rendered vars via .Vars. Traits are
// available as .Traits. If varOrder is nil, all vars are still rendered but
// in non-deterministic map order. Call this BEFORE resource attribute
// rendering so that rendered vars are available to resource templates.
func RenderVars(vars map[string]any, traits map[string]any, varOrder []string) (map[string]any, error) {
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
		rendered, err := RenderTemplate(strVal, traits, result)
		if err != nil {
			return nil, fmt.Errorf("rendering var %q: %w", k, err)
		}
		result[k] = rendered
	}
	return result, nil
}

// containsWord checks if s contains the exact substring sub.
func containsWord(s, sub string) bool {
	return strings.Contains(s, sub)
}
