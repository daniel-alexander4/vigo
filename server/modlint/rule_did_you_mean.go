// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"sort"

	vigovalidate "git.alexander4.io/vigo/server/validate"
)

// didYouMeanRule emits Hint-level suggestions whenever a resource has
// an unknown type or an unknown attribute. The validate-adapter rule
// has already produced the corresponding Error; this rule's job is to
// add the "did you mean … ?" line that turns a frustrating typo into
// a one-character fix.
//
// Match logic: Levenshtein distance ≤ 2 over lowercase, picking the
// closest known string. Distance 0 means the type/attribute is in fact
// recognized — the validate adapter wouldn't have flagged it — so we
// skip. Above distance 2 we stay quiet rather than guess noisily.
//
// Severity: Hint. Operators reading the output already know the type
// or attribute is unknown (the validate adapter said so); the
// suggestion is purely advisory.
type didYouMeanRule struct{}

func (didYouMeanRule) id() string { return "did-you-mean" }

// universalAttrs duplicates the small allowlist in server/validate so
// modlint doesn't have to widen validate's exported surface for one
// stable lookup table. Keep in sync with validate.universalAttrs.
var universalAttrs = map[string]bool{
	"state": true, "onlyif": true, "unless": true, "creates": true,
	"skip_validation": true, "when": true, "timeout_secs": true,
	"case": true, "match": true, "watch_secret": true,
}

func (didYouMeanRule) apply(mod *configcrate, _ Options) []Finding {
	var out []Finding
	for _, res := range mod.Resources {
		// Unknown resource type → suggest from KnownTypes.
		if res.Type != "" && !vigovalidate.KnownTypes[res.Type] {
			if guess := closestKey(res.Type, knownTypeNames()); guess != "" {
				out = append(out, Finding{
					Severity: Hint,
					Line:     res.Line,
					Column:   res.Column,
					RuleID:   "did-you-mean",
					Message: fmt.Sprintf(
						"resource %q: type %q is unknown — did you mean %q?",
						res.Name, res.Type, guess,
					),
				})
			}
			continue // Don't probe attribute names against a non-schema.
		}

		// Unknown attribute → suggest from this type's schema.
		schema, ok := vigovalidate.TypeSchemas[res.Type]
		if !ok || schema.AllowExtra || schema.Attrs == nil {
			continue
		}
		for _, key := range res.AttrOrder {
			if universalAttrs[key] {
				continue
			}
			if _, known := schema.Attrs[key]; known {
				continue
			}
			if guess := closestKey(key, schemaAttrNames(schema)); guess != "" {
				out = append(out, Finding{
					Severity: Hint,
					Line:     res.Line,
					Column:   res.Column,
					RuleID:   "did-you-mean",
					Message: fmt.Sprintf(
						"resource %q: attribute %q is unknown for %s — did you mean %q?",
						res.Name, key, res.Type, guess,
					),
				})
			}
		}
	}
	return out
}

// knownTypeNames returns a sorted slice of known resource type names.
// Sorted so suggestion choice is deterministic on ties (alphabetical
// wins). Built fresh each rule pass — the typecount is small and
// caching across calls would force lifecycle gymnastics for unit tests.
func knownTypeNames() []string {
	out := make([]string, 0, len(vigovalidate.KnownTypes))
	for k := range vigovalidate.KnownTypes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func schemaAttrNames(s vigovalidate.TypeSchema) []string {
	out := make([]string, 0, len(s.Attrs))
	for k := range s.Attrs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// closestKey returns the candidate with the smallest Levenshtein
// distance to `s`, provided that distance is in [1, 2]. Distance 0
// (exact match) returns "" — the caller never reaches this rule for
// known names. Distance > 2 returns "" so we don't surface noisy
// guesses for genuinely-unrelated typos.
func closestKey(s string, candidates []string) string {
	best := ""
	bestDist := 3 // anything ≥ 3 is too far
	for _, c := range candidates {
		d := levenshtein(s, c)
		if d > 0 && d < bestDist {
			bestDist = d
			best = c
		}
	}
	return best
}

// levenshtein returns the edit distance between a and b. Iterative
// two-row implementation; O(len(a)*len(b)) time, O(min(len)) space.
// Stable-suitable for the short strings in scope here (type names,
// attribute names — both rarely > 30 chars).
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// Ensure b is the shorter so we allocate the smaller slice.
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = del
			if ins < curr[j] {
				curr[j] = ins
			}
			if sub < curr[j] {
				curr[j] = sub
			}
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func init() { register(didYouMeanRule{}) }
