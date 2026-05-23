// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

// moduleComplianceKeyRetiredRule errors when a per-module YAML
// declares a `compliance:` block at the top level. Retired in 0.66.48
// as part of the compliance-authoring fold-down — per-module surface
// is `provides:` / `bundle:` exclusively now. The directory-level
// `compliance.vgo` inheritance mechanism still accepts `compliance:`
// blocks (that's a different file shape, not a module YAML), so the
// rule only fires on modules (anything with a `name:` field that
// isn't a compliance.vgo).
//
// The loader silently skips the YAML key at parse time
// (server/config/parse.go) to let this rule produce the pointed
// error message rather than a generic YAML decode failure.
type moduleComplianceKeyRetiredRule struct{}

func (moduleComplianceKeyRetiredRule) id() string { return "module-compliance-key-retired" }

func (moduleComplianceKeyRetiredRule) apply(mod *module, _ Options) []Finding {
	if len(mod.Compliance) == 0 {
		return nil
	}
	return []Finding{{
		Severity: Error,
		Line:     mod.Line,
		Column:   mod.Column,
		RuleID:   "module-compliance-key-retired",
		Message: "per-module `compliance:` is retired (0.66.48) — replace with `provides: [<tag>]` (functional capability) or `bundle: [<id>]` (framework scope-cut). " +
			"Run `vigocli compliance provisions` and `vigocli compliance bundles` to list catalog tags. " +
			"Directory-level `compliance.vgo` files still accept `compliance:` claims for inheritance.",
	}}
}

func init() { register(moduleComplianceKeyRetiredRule{}) }
