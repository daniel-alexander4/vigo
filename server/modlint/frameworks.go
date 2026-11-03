// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/frameworks"
)

// canonicalFrameworkSlugs is the shared lookup table for every modlint
// surface that validates or canonicalizes a framework name (the
// standard-key form — e.g. "cis-rhel", "hipaa"). Used today by
// `waiversfile.go` (waivers in compliance.vgo files reference framework
// slugs); the per-module `compliance:` canonicalization rule that
// previously lived here was retired in 0.66.48 along with the per-module
// `compliance:` key itself (operators now write `provides:` / `bundle:`,
// both of which reference Go-typed catalogs where framework typos are
// compile-checked).
//
// Derived from the framework registry (server/frameworks) — the single
// source of truth — and sorted so diffs read cleanly.
var canonicalFrameworkSlugs = func() []string {
	out := make([]string, 0, len(frameworks.All))
	for _, f := range frameworks.All {
		out = append(out, f.StandardKey)
	}
	sort.Strings(out)
	return out
}()

// canonicalFrameworkSet is the same list as a lookup table.
var canonicalFrameworkSet = func() map[string]bool {
	m := make(map[string]bool, len(canonicalFrameworkSlugs))
	for _, s := range canonicalFrameworkSlugs {
		m[s] = true
	}
	return m
}()

// canonicalize normalizes an operator-typed framework name.
// Lowercases, converts spaces and underscores to hyphens, collapses
// runs of hyphens. Doesn't validate — just produces the comparable
// form.
func canonicalize(name string) string {
	out := strings.ToLower(name)
	out = strings.ReplaceAll(out, "_", "-")
	out = strings.ReplaceAll(out, " ", "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}
