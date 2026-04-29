// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"sort"
	"strings"
)

// complianceFrameworkRule canonicalizes the framework keys under a
// module's `compliance:` block.
//
// The canonical slug is lowercase, hyphen-separated, ASCII. Operators
// often type "HIPAA" or "SOC 2" or "PCI-DSS" or "NIST 800-53" — any of
// those misses the lookup in `server/api/report.go` because the
// catalog is keyed on the slug. A typo'd name silently de-tags the
// module from the framework — coverage scoring drops to zero on that
// envoy and nobody notices until an audit.
//
// Auto-fix: lowercase, normalize spaces/underscores to hyphens, then
// match against the canonical list. Exact match → rename the key in
// the parsed module (Warning, FixApplied=true). No match → look for
// the closest known slug via Levenshtein (Error with did-you-mean).
//
// Inline catalog: keeping the list here rather than importing from
// server/api preserves modlint's "no server deps" rule. The list
// changes rarely (frameworks are added across releases, not days);
// when one does land, both this file and the catalog in report.go
// must be updated together.
type complianceFrameworkRule struct{}

func (complianceFrameworkRule) id() string { return "compliance-framework" }

// canonicalFrameworkSlugs is the source-of-truth list. Mirrors the
// build*Findings methods in server/api/report.go. Sorted; keep
// alphabetical so diffs read cleanly when adding a framework.
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

// canonicalize normalizes an operator-typed framework name. Lowercases,
// converts spaces and underscores to hyphens, collapses runs of
// hyphens. Doesn't validate — just produces the comparable form.
func canonicalize(name string) string {
	out := strings.ToLower(name)
	out = strings.ReplaceAll(out, "_", "-")
	out = strings.ReplaceAll(out, " ", "-")
	// Collapse runs of '-' so "PCI--DSS" → "pci-dss".
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

func (complianceFrameworkRule) apply(mod *module, _ Options) []Finding {
	if len(mod.Compliance) == 0 {
		return nil
	}
	var out []Finding

	// Build renames first so we don't mutate the map while iterating.
	type rename struct{ from, to string }
	var renames []rename
	for original := range mod.Compliance {
		canon := canonicalize(original)
		if original == canon && canonicalFrameworkSet[canon] {
			continue // already canonical
		}
		if canonicalFrameworkSet[canon] {
			renames = append(renames, rename{from: original, to: canon})
			out = append(out, Finding{
				Severity:   Warning,
				Line:       mod.Line,
				Column:     mod.Column,
				RuleID:     "compliance-framework",
				FixApplied: true,
				Message: fmt.Sprintf(
					"compliance framework %q normalized to %q (canonical slug)",
					original, canon,
				),
			})
			continue
		}
		// Unknown framework — try did-you-mean.
		if guess := closestKey(canon, canonicalFrameworkSlugs); guess != "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "compliance-framework",
				Message: fmt.Sprintf(
					"compliance framework %q is not recognized — did you mean %q?",
					original, guess,
				),
			})
		} else {
			out = append(out, Finding{
				Severity: Error,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "compliance-framework",
				Message: fmt.Sprintf(
					"compliance framework %q is not recognized — must be one of: %s",
					original, strings.Join(canonicalFrameworkSlugs, ", "),
				),
			})
		}
	}

	// Apply renames in deterministic order so the rendered output is
	// stable even when multiple frameworks need correction.
	sort.Slice(renames, func(i, j int) bool { return renames[i].from < renames[j].from })
	for _, r := range renames {
		controls := mod.Compliance[r.from]
		delete(mod.Compliance, r.from)
		mod.Compliance[r.to] = controls
		// Update ComplianceOrder so renderer keeps source-order.
		for i, k := range mod.ComplianceOrder {
			if k == r.from {
				mod.ComplianceOrder[i] = r.to
				break
			}
		}
	}
	return out
}

func init() { register(complianceFrameworkRule{}) }
