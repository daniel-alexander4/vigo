// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"strings"
)

// ExpandForEach expands resources with ForEach set into N resources by
// iterating the named var list. For string lists, {{ .Item }} is substituted.
// For list-of-maps, {{ .Each.key }} is substituted with the map value for
// each key. Substitution applies to name, when, and all string attribute
// values. Resources without ForEach pass through.
func ExpandForEach(resources []ResourceConfig, vars map[string]any) ([]ResourceConfig, error) {
	var result []ResourceConfig
	for _, res := range resources {
		if res.ForEach == "" {
			result = append(result, res)
			continue
		}

		raw, ok := vars[res.ForEach]
		if !ok {
			return nil, fmt.Errorf("resource %q: foreach var %q not found", res.Name, res.ForEach)
		}

		items, err := toSlice(raw)
		if err != nil {
			return nil, fmt.Errorf("resource %q: foreach var %q: %w", res.Name, res.ForEach, err)
		}

		for _, item := range items {
			clone := cloneResource(res)
			clone.ForEach = ""

			switch v := item.(type) {
			case map[string]any:
				// Map item: substitute {{ .Each.key }} patterns.
				clone.Name = substituteEach(clone.Name, v)
				clone.When = substituteEach(clone.When, v)
				for k, attr := range clone.Attributes {
					if s, ok := attr.(string); ok {
						clone.Attributes[k] = substituteEach(s, v)
					}
				}
			default:
				// Scalar item: substitute {{ .Item }}.
				s := fmt.Sprintf("%v", v)
				clone.Name = substituteItem(clone.Name, s)
				clone.When = substituteItem(clone.When, s)
				for k, attr := range clone.Attributes {
					if str, ok := attr.(string); ok {
						clone.Attributes[k] = substituteItem(str, s)
					}
				}
			}

			result = append(result, clone)
		}
	}
	return result, nil
}

// ExpandCase resolves case/match on resources using traits and vars.
// The case expression is rendered as a Go template. The matching branch's
// attrs are merged into the resource. Falls back to "default" branch.
func ExpandCase(resources []ResourceConfig, traits map[string]any, vars map[string]any) ([]ResourceConfig, error) {
	result := make([]ResourceConfig, 0, len(resources))
	for _, res := range resources {
		if res.Case == "" {
			result = append(result, res)
			continue
		}

		rendered, err := RenderTemplate(res.Case, traits, vars)
		if err != nil {
			return nil, fmt.Errorf("resource %q: rendering case expression: %w", res.Name, err)
		}
		rendered = strings.TrimSpace(rendered)

		branch, ok := res.CaseMatch[rendered]
		if !ok {
			branch, ok = res.CaseMatch["default"]
			if !ok {
				return nil, fmt.Errorf("resource %q: no match for case %q and no default branch", res.Name, rendered)
			}
		}

		// Merge matched branch attrs into resource attributes.
		if res.Attributes == nil {
			res.Attributes = make(map[string]any)
		}
		for k, v := range branch {
			res.Attributes[k] = v
		}
		res.Case = ""
		res.CaseMatch = nil

		result = append(result, res)
	}
	return result, nil
}

// HasCaseResources returns true if any module in the config has resources
// with Case expressions, meaning per-envoy expansion is needed at check-in.
func HasCaseResources(cfg *EnvoyConfig) bool {
	for _, mod := range cfg.Modules {
		for _, res := range mod.Resources {
			if res.Case != "" {
				return true
			}
		}
	}
	return false
}

// substituteItem replaces {{ .Item }} (with flexible whitespace) in s.
func substituteItem(s, item string) string {
	// Handle common whitespace variants.
	s = strings.ReplaceAll(s, "{{ .Item }}", item)
	s = strings.ReplaceAll(s, "{{.Item}}", item)
	s = strings.ReplaceAll(s, "{{ .Item}}", item)
	s = strings.ReplaceAll(s, "{{.Item }}", item)
	return s
}

// substituteEach replaces {{ .Each.key }} and {{ .Item.key }} patterns
// using map values. Both prefixes are supported for convenience.
func substituteEach(s string, m map[string]any) string {
	for k, v := range m {
		val := fmt.Sprintf("%v", v)
		for _, prefix := range []string{".Each.", ".Item."} {
			s = strings.ReplaceAll(s, "{{ "+prefix+k+" }}", val)
			s = strings.ReplaceAll(s, "{{"+prefix+k+"}}", val)
			s = strings.ReplaceAll(s, "{{ "+prefix+k+"}}", val)
			s = strings.ReplaceAll(s, "{{"+prefix+k+" }}", val)
		}
	}
	return s
}

// toSlice coerces a var value to a []any for foreach iteration.
func toSlice(v any) ([]any, error) {
	switch val := v.(type) {
	case []any:
		return val, nil
	case []string:
		result := make([]any, len(val))
		for i, s := range val {
			result[i] = s
		}
		return result, nil
	default:
		return nil, fmt.Errorf("expected a list, got %T", v)
	}
}

// cloneResource returns a deep copy of a ResourceConfig.
func cloneResource(r ResourceConfig) ResourceConfig {
	clone := r
	if r.Attributes != nil {
		clone.Attributes = make(map[string]any, len(r.Attributes))
		for k, v := range r.Attributes {
			clone.Attributes[k] = v
		}
	}
	if r.DependsOn != nil {
		clone.DependsOn = append([]string(nil), r.DependsOn...)
	}
	if r.Notify != nil {
		clone.Notify = append([]string(nil), r.Notify...)
	}
	if r.Subscribes != nil {
		clone.Subscribes = append([]string(nil), r.Subscribes...)
	}
	if r.Before != nil {
		clone.Before = append([]string(nil), r.Before...)
	}
	if r.CaseMatch != nil {
		clone.CaseMatch = make(map[string]map[string]any, len(r.CaseMatch))
		for k, v := range r.CaseMatch {
			branch := make(map[string]any, len(v))
			for bk, bv := range v {
				branch[bk] = bv
			}
			clone.CaseMatch[k] = branch
		}
	}
	return clone
}
