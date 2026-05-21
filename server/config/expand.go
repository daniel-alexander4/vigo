// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"log/slog"
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
		if _, isFleet := raw.(*FleetLookup); isFleet {
			// foreach: over a fleet: var — the var is still unresolved at config
			// load. Leave the resource for the deferred ExpandForEach the
			// per-envoy clone path runs after ResolveFleetLookupsInConfig. (The
			// fleet: lookup must not set join: for this — foreach: needs a list.)
			result = append(result, res)
			continue
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

// HasModuleWhens returns true if any module in the config has a module-level
// when: expression. Such modules may be filtered during bundle building based
// on traits, making the load-time Merkle root unreliable for fast-path comparison.
func HasModuleWhens(cfg *EnvoyConfig) bool {
	for _, mod := range cfg.Modules {
		if mod.When != "" {
			return true
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

// foreachModuleExpansionWarnThreshold: a foreach: ref that expands into more
// module instances than this logs a warning — every instance is its own module
// in the policy bundle, so it costs wire bytes, server memory, and a per-module
// agent cache entry on every check-in.
const foreachModuleExpansionWarnThreshold = 200

// expandForeachModuleRefs replaces any ModuleRef carrying foreach: with one
// synthetic per-item ref named "<module>[<key>]". The list comes from vars; the
// item's fields land on the instance as {{ .Each.<field> }} (maps) or
// {{ .Item }} (scalars) — see expandModuleInstanceResources. A scalar list is
// keyed by the value itself; a list of maps requires key: on the ref naming
// which field supplies the suffix. Duplicate resolved keys are an error.
func expandForeachModuleRefs(refs []ModuleRef, defs map[string]*ModuleDefinition, vars map[string]any) ([]ModuleRef, error) {
	anyForeach := false
	for _, r := range refs {
		if r.ForEach != "" {
			anyForeach = true
			break
		}
	}
	if !anyForeach {
		return refs, nil
	}

	out := make([]ModuleRef, 0, len(refs))
	for _, r := range refs {
		if r.ForEach == "" {
			out = append(out, r)
			continue
		}
		raw, ok := vars[r.ForEach]
		if !ok {
			return nil, fmt.Errorf("module %q: foreach var %q not found", r.Name, r.ForEach)
		}
		items, err := toSlice(raw)
		if err != nil {
			return nil, fmt.Errorf("module %q: foreach var %q: %w", r.Name, r.ForEach, err)
		}
		seenKeys := make(map[string]bool, len(items))
		for _, item := range items {
			key, kerr := foreachInstanceKey(item, r.Key)
			if kerr != nil {
				return nil, fmt.Errorf("module %q foreach %q: %w", r.Name, r.ForEach, kerr)
			}
			if seenKeys[key] {
				return nil, fmt.Errorf("module %q foreach %q: two items resolve to the same instance key %q — keys must be unique", r.Name, r.ForEach, key)
			}
			seenKeys[key] = true
			out = append(out, ModuleRef{
				Name:        r.Name + "[" + key + "]",
				When:        r.When,
				BaseName:    r.Name,
				EachItem:    item,
				InstanceKey: key,
			})
		}
		if n := len(items); n > foreachModuleExpansionWarnThreshold {
			slog.Warn("foreach: expands into a large number of module instances — large policy bundle",
				"module", r.Name, "var", r.ForEach, "count", n, "threshold", foreachModuleExpansionWarnThreshold)
		}
	}
	return out, nil
}

// foreachInstanceKey resolves the instance suffix for one foreach item.
func foreachInstanceKey(item any, keyField string) (string, error) {
	switch v := item.(type) {
	case map[string]any:
		if keyField == "" {
			return "", fmt.Errorf("the list contains maps — add key: <field> on the module ref to name which item field supplies the instance suffix")
		}
		kv, ok := v[keyField]
		if !ok {
			return "", fmt.Errorf("an item is missing the %q field named by the key: setting", keyField)
		}
		return fmt.Sprintf("%v", kv), nil
	default:
		if keyField != "" {
			return "", fmt.Errorf("key: %q was given but the list contains scalars, not maps — drop key: for a scalar list", keyField)
		}
		return fmt.Sprintf("%v", item), nil
	}
}

// expandModuleInstanceResources substitutes a foreach item's fields into a
// module instance's resources ({{ .Each.<field> }} for maps, {{ .Item }} for
// scalars), then suffixes every resource name with [<key>] so instances don't
// collide, rewriting intra-module depends_on/notify/subscribes/before
// references to the suffixed names. A resource carrying its own foreach: is
// left for the resource-level expansion (its .Each/.Item is its own item).
func expandModuleInstanceResources(resources []ResourceConfig, item any, key string) ([]ResourceConfig, error) {
	suffix := "[" + key + "]"
	itemMap, _ := item.(map[string]any)
	itemStr := fmt.Sprintf("%v", item)

	// Pass 1: substitute the item into name/when/attrs and dependency refs.
	for i := range resources {
		r := &resources[i]
		if r.ForEach != "" {
			continue // nested foreach: leave .Each/.Item for ExpandForEach
		}
		if itemMap != nil {
			r.Name = substituteEach(r.Name, itemMap)
			r.When = substituteEach(r.When, itemMap)
			for k, attr := range r.Attributes {
				if s, ok := attr.(string); ok {
					r.Attributes[k] = substituteEach(s, itemMap)
				}
			}
			r.DependsOn = substituteEachAll(r.DependsOn, itemMap)
			r.Notify = substituteEachAll(r.Notify, itemMap)
			r.Subscribes = substituteEachAll(r.Subscribes, itemMap)
			r.Before = substituteEachAll(r.Before, itemMap)
		} else {
			r.Name = substituteItem(r.Name, itemStr)
			r.When = substituteItem(r.When, itemStr)
			for k, attr := range r.Attributes {
				if s, ok := attr.(string); ok {
					r.Attributes[k] = substituteItem(s, itemStr)
				}
			}
			r.DependsOn = substituteItemAll(r.DependsOn, itemStr)
			r.Notify = substituteItemAll(r.Notify, itemStr)
			r.Subscribes = substituteItemAll(r.Subscribes, itemStr)
			r.Before = substituteItemAll(r.Before, itemStr)
		}
		// A leftover .Each/.Item reference means the item lacks a field the
		// module asked for — fail loudly at load rather than letting the agent
		// silently drop the resource on a template error.
		if hasEachOrItemRef(r.Name) {
			return nil, fmt.Errorf("resource %q references an .Each/.Item field this foreach item doesn't provide", r.Name)
		}
		if hasEachOrItemRef(r.When) {
			return nil, fmt.Errorf("resource %q: when: references an .Each/.Item field this foreach item doesn't provide", r.Name)
		}
		for k, attr := range r.Attributes {
			if s, ok := attr.(string); ok && hasEachOrItemRef(s) {
				return nil, fmt.Errorf("resource %q: attribute %q references an .Each/.Item field this foreach item doesn't provide", r.Name, k)
			}
		}
	}

	// Pass 2: collect the (now substituted) resource names.
	names := make(map[string]bool, len(resources))
	for i := range resources {
		names[resources[i].Name] = true
	}
	rewriteRefs := func(refs []string) []string {
		if len(refs) == 0 {
			return refs
		}
		out := make([]string, len(refs))
		for i, s := range refs {
			if names[s] {
				out[i] = s + suffix
			} else {
				out[i] = s
			}
		}
		return out
	}

	// Pass 3: suffix names and intra-module references.
	for i := range resources {
		r := &resources[i]
		r.DependsOn = rewriteRefs(r.DependsOn)
		r.Notify = rewriteRefs(r.Notify)
		r.Subscribes = rewriteRefs(r.Subscribes)
		r.Before = rewriteRefs(r.Before)
		r.Name = r.Name + suffix
	}
	return resources, nil
}

func substituteEachAll(refs []string, m map[string]any) []string {
	if len(refs) == 0 {
		return refs
	}
	out := make([]string, len(refs))
	for i, s := range refs {
		out[i] = substituteEach(s, m)
	}
	return out
}

func substituteItemAll(refs []string, item string) []string {
	if len(refs) == 0 {
		return refs
	}
	out := make([]string, len(refs))
	for i, s := range refs {
		out[i] = substituteItem(s, item)
	}
	return out
}

// hasEachOrItemRef reports whether s still contains an unresolved
// {{ .Each.<...> }} / {{ .Item.<...> }} / {{ .Item }} template reference.
func hasEachOrItemRef(s string) bool {
	if !strings.Contains(s, "{{") {
		return false
	}
	return strings.Contains(s, ".Each.") || strings.Contains(s, ".Each }") || strings.Contains(s, ".Each}") ||
		strings.Contains(s, ".Item.") || strings.Contains(s, ".Item }") || strings.Contains(s, ".Item}")
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
