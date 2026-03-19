// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"strings"
)

// ExpandForEach expands resources with ForEach set into N resources by
// iterating the named var list. {{ .Item }} is substituted in name, when,
// and all string attribute values. Resources without ForEach pass through.
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

		items, err := toStringSlice(raw)
		if err != nil {
			return nil, fmt.Errorf("resource %q: foreach var %q: %w", res.Name, res.ForEach, err)
		}

		for _, item := range items {
			clone := cloneResource(res)
			clone.ForEach = ""
			clone.Name = substituteItem(clone.Name, item)
			clone.When = substituteItem(clone.When, item)
			for k, v := range clone.Attributes {
				if s, ok := v.(string); ok {
					clone.Attributes[k] = substituteItem(s, item)
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

// toStringSlice coerces a var value to a []string for foreach iteration.
func toStringSlice(v any) ([]string, error) {
	switch val := v.(type) {
	case []any:
		result := make([]string, len(val))
		for i, item := range val {
			result[i] = fmt.Sprintf("%v", item)
		}
		return result, nil
	case []string:
		return val, nil
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
