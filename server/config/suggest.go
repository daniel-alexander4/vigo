// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"sort"
	"strings"
)

// suggestion returns a hint fragment to append to a "does not exist"
// error for a reference name that couldn't be found in a pool of
// candidate names. The returned string is either empty (no useful
// hint) or a fragment beginning with " — " that is ready to be
// appended to the caller's error message.
//
// Two heuristics are applied, in order:
//
//  1. The `.vgo` suffix trap: if ref ends in ".vgo" and stripping
//     the extension matches a candidate exactly, say so outright.
//     This is the most common operator footgun — copying the
//     filename into a modules: or roles: list instead of the
//     `name:` field value.
//  2. Fuzzy match: case-insensitive Levenshtein distance ≤ 2
//     against the candidate set. Catches typos like
//     "base-firwall" → "base-firewall".
func suggestion(ref string, candidates []string) string {
	if strings.HasSuffix(ref, ".vgo") {
		stripped := strings.TrimSuffix(ref, ".vgo")
		for _, c := range candidates {
			if c == stripped {
				return fmt.Sprintf(` — did you mean %q? References use the "name:" field, not the filename`, stripped)
			}
		}
	}

	refLC := strings.ToLower(ref)
	best := ""
	bestDist := 3 // require distance ≤ 2
	for _, c := range candidates {
		d := levenshtein(refLC, strings.ToLower(c))
		if d < bestDist {
			bestDist = d
			best = c
		}
	}
	if best != "" {
		return fmt.Sprintf(` — did you mean %q?`, best)
	}
	return ""
}

// notFoundHint builds the suffix appended to a "does not exist"
// error. When a suggestion is available it replaces the generic
// fallback hint; otherwise the fallback hint is returned prefixed
// with " — " (or the empty string if no fallback was supplied).
func notFoundHint(ref string, candidates []string, fallback string) string {
	if s := suggestion(ref, candidates); s != "" {
		return s
	}
	if fallback != "" {
		return " — " + fallback
	}
	return ""
}

// moduleCandidates returns a sorted slice of module names from a
// module definition map, for deterministic suggestion ordering.
func moduleCandidates(defs map[string]*ConfigcrateDefinition) []string {
	out := make([]string, 0, len(defs))
	for k := range defs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// roleCandidates returns a sorted slice of role names from a role
// definition map, for deterministic suggestion ordering.
func roleCandidates(defs map[string]*RoleDefinition) []string {
	out := make([]string, 0, len(defs))
	for k := range defs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// levenshtein returns the edit distance between two strings using
// the standard O(n·m) dynamic-programming table. Candidate sets for
// modules/roles/envoys are small (typically <200), so the quadratic
// cost is irrelevant.
func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := 0; j <= len(rb); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
