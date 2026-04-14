// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MatchEnvoy reports whether a target spec matches a specific envoy described
// by its hostname, tags, and traits. It implements the same spec disambiguation
// as the admin-side targeting resolver (server/grpc/targeting.go) so operators
// only need to learn one syntax:
//
//   - "tag:<name>"     → true if tag is present in tags (tag name may be a glob)
//   - "<key>=<value>"  → true if trait key equals value (trait filter)
//   - "<glob>"         → true if hostname matches the glob (contains * or ?)
//   - "<name>"         → true if hostname equals name (exact)
//
// Used by the var-lookup resolver to pick an arm from tag/host/trait-keyed
// lookup tables at check-in time.
func MatchEnvoy(spec, hostname string, tags []string, traits map[string]string) (bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return false, fmt.Errorf("target spec is empty")
	}

	// Tag filter: tag:<name>. Name may contain glob metachars.
	if strings.HasPrefix(spec, "tag:") {
		want := strings.TrimSpace(spec[4:])
		if want == "" {
			return false, fmt.Errorf("tag filter is empty")
		}
		hasGlob := strings.ContainsAny(want, "*?")
		for _, t := range tags {
			if hasGlob {
				matched, err := filepath.Match(want, t)
				if err != nil {
					return false, fmt.Errorf("invalid tag glob %q: %w", want, err)
				}
				if matched {
					return true, nil
				}
			} else if t == want {
				return true, nil
			}
		}
		return false, nil
	}

	// Trait filter: key=value. Key is a dot path.
	if idx := strings.Index(spec, "="); idx >= 0 {
		key := strings.TrimSpace(spec[:idx])
		want := strings.TrimSpace(spec[idx+1:])
		if key == "" {
			return false, fmt.Errorf("trait filter key is empty")
		}
		got, ok := traits[key]
		return ok && got == want, nil
	}

	// Hostname glob.
	if strings.ContainsAny(spec, "*?") {
		matched, err := filepath.Match(spec, hostname)
		if err != nil {
			return false, fmt.Errorf("invalid hostname glob %q: %w", spec, err)
		}
		return matched, nil
	}

	// Exact hostname.
	return spec == hostname, nil
}

// specSpecificity ranks a target spec so that, when multiple arms of a
// scalar lookup match, the most specific wins. Higher is more specific.
// The ranking is:
//
//	exact hostname     : 40
//	trait filter       : 30
//	tag                : 20
//	hostname glob      : 10
//
// Ties within a rank are broken by declaration order (caller's responsibility).
func specSpecificity(spec string) int {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "tag:") {
		return 20
	}
	if strings.Contains(spec, "=") {
		return 30
	}
	if strings.ContainsAny(spec, "*?") {
		return 10
	}
	return 40
}

// IsLookupTable reports whether a YAML-decoded value is a tag/host/trait
// lookup table. A lookup table is a map[string]any containing a "default"
// key — the default key is the discriminator that tells the resolver
// "this map is a lookup table, not a literal map value to pass through."
func IsLookupTable(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, hasDefault := m["default"]
	return hasDefault
}

// ResolveLookup picks the arm of a lookup table that matches the given
// envoy attributes and returns its value. Key syntax mirrors the targeting
// resolver (tag:, host, glob, trait=value). Precedence:
//
//   - For list-valued lookups: UNION all matching arms (order-independent).
//   - For scalar-valued lookups: most-specific-wins, where specificity is
//     exact hostname > trait filter > tag > hostname glob. Ties broken
//     by declaration order.
//   - Mixed-type arms (some list, some scalar) return an error at resolve
//     time — this is a config bug and should have been caught earlier.
//   - If no arm matches, "default" is used.
//
// keyOrder records the declaration order of the keys; when keyOrder is
// nil the map iteration order is used (non-deterministic for ties).
func ResolveLookup(
	table map[string]any,
	keyOrder []string,
	hostname string,
	tags []string,
	traits map[string]string,
) (any, error) {
	defaultVal, hasDefault := table["default"]
	if !hasDefault {
		return nil, fmt.Errorf("lookup table missing required 'default' key")
	}

	// Determine whether values are lists or scalars. Mixed = error.
	// "default" participates in the type check too — a scalar default
	// means scalar arms, a list default means list arms.
	isList := isListValue(defaultVal)
	for k, v := range table {
		if k == "default" {
			continue
		}
		if isListValue(v) != isList {
			return nil, fmt.Errorf("lookup table has mixed-type arms (some lists, some scalars); arm %q differs from default", k)
		}
	}

	// Walk keys in declaration order when provided. This gives stable
	// tiebreaks and deterministic error messages.
	keys := keyOrder
	if keys == nil {
		keys = make([]string, 0, len(table))
		for k := range table {
			keys = append(keys, k)
		}
	}

	type match struct {
		spec        string
		value       any
		specificity int
		order       int
	}
	var matches []match
	for i, k := range keys {
		if k == "default" {
			continue
		}
		v, ok := table[k]
		if !ok {
			continue
		}
		ok, err := MatchEnvoy(k, hostname, tags, traits)
		if err != nil {
			return nil, fmt.Errorf("lookup arm %q: %w", k, err)
		}
		if ok {
			matches = append(matches, match{spec: k, value: v, specificity: specSpecificity(k), order: i})
		}
	}

	if len(matches) == 0 {
		return defaultVal, nil
	}

	if isList {
		// Union all matching arms in declaration order.
		var out []any
		seen := make(map[any]bool)
		for _, m := range matches {
			list, _ := m.value.([]any)
			for _, item := range list {
				// Use the item itself as the dedupe key. Works for
				// strings/ints/bools — the usual scalar list elements
				// vigo configs care about. Maps and slices are not
				// hashable and skip dedup.
				if isHashable(item) {
					if seen[item] {
						continue
					}
					seen[item] = true
				}
				out = append(out, item)
			}
		}
		return out, nil
	}

	// Scalar: most-specific-wins, ties broken by declaration order.
	best := matches[0]
	for _, m := range matches[1:] {
		if m.specificity > best.specificity ||
			(m.specificity == best.specificity && m.order < best.order) {
			best = m
		}
	}
	return best.value, nil
}

func isListValue(v any) bool {
	_, ok := v.([]any)
	return ok
}

func isHashable(v any) bool {
	switch v.(type) {
	case string, bool, int, int64, float64:
		return true
	}
	return false
}

// DetectLookupsInValue recursively walks a YAML-decoded value and returns
// true if any map[string]any inside is a lookup table (has a "default" key).
// Used at config load time to set EnvoyConfig.HasLookups so the check-in
// path knows to clone the config and resolve lookups per-envoy.
func DetectLookupsInValue(v any) bool {
	if IsLookupTable(v) {
		return true
	}
	switch x := v.(type) {
	case map[string]any:
		for _, vv := range x {
			if DetectLookupsInValue(vv) {
				return true
			}
		}
	case []any:
		for _, vv := range x {
			if DetectLookupsInValue(vv) {
				return true
			}
		}
	}
	return false
}

// ResolveLookupsInAttributes walks a resource's attribute map and replaces
// any top-level lookup-table values with the per-envoy resolved value.
// Only top-level attributes are inspected — lookups must appear directly
// as the value of a resource field, not nested inside other structures.
func ResolveLookupsInAttributes(attrs map[string]any, hostname string, tags []string, traits map[string]string) error {
	for k, v := range attrs {
		if !IsLookupTable(v) {
			continue
		}
		resolved, err := ResolveLookup(v.(map[string]any), nil, hostname, tags, traits)
		if err != nil {
			return fmt.Errorf("attribute %q: %w", k, err)
		}
		attrs[k] = resolved
	}
	return nil
}

// ResolveLookupsInConfig walks a cloned EnvoyConfig and resolves every
// resource-attribute lookup table against the given envoy attributes.
// Mutates the passed config — callers must pass a Clone, not the shared
// store entry.
func ResolveLookupsInConfig(cfg *EnvoyConfig, hostname string, traits map[string]string) error {
	for mi := range cfg.Modules {
		for ri := range cfg.Modules[mi].Resources {
			attrs := cfg.Modules[mi].Resources[ri].Attributes
			if attrs == nil {
				continue
			}
			if err := ResolveLookupsInAttributes(attrs, hostname, cfg.Tags, traits); err != nil {
				return fmt.Errorf("module %q resource %q: %w",
					cfg.Modules[mi].Name,
					cfg.Modules[mi].Resources[ri].Name,
					err)
			}
		}
	}
	return nil
}
