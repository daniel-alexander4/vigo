// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import "strings"

// Canonical framework slugs — kept here as a shared lookup table for
// every modlint surface that needs to validate or canonicalize a
// framework name. Used today by `waiversfile.go` (waivers in
// compliance.vgo files reference framework slugs); the per-module
// `compliance:` canonicalization rule that previously lived here was
// retired in 0.66.48 along with the per-module `compliance:` key
// itself (operators now write `provides:` / `bundle:`, both of which
// reference Go-typed catalogs where framework typos are
// compile-checked).
//
// Sorted; keep alphabetical so diffs read cleanly when adding a
// framework. Mirrors the canonical list in server/api/report.go +
// server/config/loader.go.
var canonicalFrameworkSlugs = []string{
	"aba-cyber",
	"ccss",
	"cis-rhel",
	"cis-ubuntu",
	"cis-windows",
	"cyber-essentials",
	"fda-21cfr11",
	"finra",
	"gaming",
	"gdpr",
	"hipaa",
	"hitrust",
	"iec-62443",
	"iso-27001",
	"mifid2",
	"nerc-cip",
	"nist-800-53",
	"ny-dfs-cyber",
	"pci-dss",
	"soc2",
	"sox",
}

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
