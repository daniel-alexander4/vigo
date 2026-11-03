// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"fmt"
	"sort"
	"strings"
)

// moduleComplianceKeyRetiredRule errors when a module's `compliance:`
// block carries the pre-0.66.48 framework→control list shape (e.g.
// `compliance: { cis-ubuntu: [1.7.1], nist-800-53: [AC-8] }`) instead
// of the current `{ provides: [...], bundle: [...] }` shape.
//
// History of the key:
//
//   - pre-0.66.48: per-module `compliance:` held a framework→control
//     map. Every module rehydrated the same cross-walks in inline form.
//   - 0.66.48: framework→control maps retired. Replacement keys
//     (`provides:`, `bundle:`) promoted to top-level.
//   - 0.66.57: `provides:` / `bundle:` re-folded under `compliance:` as
//     a parent block — restoring the at-a-glance compliance signal in
//     module YAML while keeping the catalog-backed cross-walk model.
//
// This rule is the gate that enforces the 0.66.57 shape against the
// pre-0.66.48 shape. Top-level `provides:` / `bundle:` (the 0.66.48
// shape) are caught by rule_configcrate_top_level_provides_bundle.
//
// Directory-level `compliance.vgo` files still carry the framework→
// control shape — that's a different file kind with its own decoder
// (server/config.loadComplianceConfigs); this rule never sees them.
type moduleComplianceKeyRetiredRule struct{}

func (moduleComplianceKeyRetiredRule) id() string { return "configcrate-compliance-key-retired" }

func (moduleComplianceKeyRetiredRule) apply(mod *configcrate, _ Options) []Finding {
	if len(mod.ComplianceUnknownSubKeys) == 0 {
		return nil
	}
	unknown := append([]string(nil), mod.ComplianceUnknownSubKeys...)
	sort.Strings(unknown)
	return []Finding{{
		Severity: Error,
		Line:     mod.Line,
		Column:   mod.Column,
		RuleID:   "configcrate-compliance-key-retired",
		Message: fmt.Sprintf(
			"`compliance:` contains framework→control entries (%s) — that shape was retired in 0.66.48. "+
				"Per-module compliance is now declared as `compliance: { provides: [<tag>], bundle: [<id>] }` "+
				"against the Vigo-curated catalogs. Run `vigocli compliance provisions` and `vigocli compliance bundles` "+
				"to list catalog tags. Directory-level `compliance.vgo` files keep the framework→control shape for inheritance.",
			strings.Join(unknown, ", "),
		),
	}}
}

func init() { register(moduleComplianceKeyRetiredRule{}) }
