// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

// requiredNameRule rejects a module that omits the top-level `name:`
// field. Usercrates are exempt — the loader defaults the name to the
// filename stem when a usercrate file omits it, so requiring `name:`
// for every usercrate would be operator-hostile.
type requiredNameRule struct{}

func (requiredNameRule) id() string { return "required-name" }

func (requiredNameRule) apply(mod *module, opts Options) []Finding {
	if mod.Name != "" || opts.IsUsercrate {
		return nil
	}
	return []Finding{{
		Severity: Error,
		Line:     mod.Line,
		Column:   mod.Column,
		RuleID:   "required-name",
		Message:  "missing required 'name' field",
	}}
}

func init() { register(requiredNameRule{}) }
