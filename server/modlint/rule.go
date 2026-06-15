// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

// rule is one lint check. Apply may mutate `mod` (auto-fix scenarios,
// e.g. injecting `when: "changed"` on a service) and emits findings
// describing what it observed.
//
// Rules must be deterministic and side-effect-free outside the passed-in
// module. They run in registration order; a rule that depends on another
// rule's output (rare) should be ordered later via its registration site.
//
// `rule` is package-private deliberately — external consumers run lint
// through `Lint()` and read `Result.Findings`. We can promote to a public
// interface if the community website ever needs to inject custom rules,
// but until that demand surfaces, keeping the registry internal lets us
// refactor freely.
type rule interface {
	id() string
	apply(mod *configcrate, opts Options) []Finding
}

// rules is the global registry, populated by per-rule init() functions.
// Each rule lives in its own file and registers itself; the registry
// makes adding a new check mechanical (drop in a new rule_*.go that
// calls register() in init()).
var rules []rule

func register(r rule) {
	rules = append(rules, r)
}

// applyRules runs every registered rule against the parsed module and
// aggregates findings. Order is registration order.
func applyRules(mod *configcrate, opts Options) []Finding {
	var out []Finding
	for _, r := range rules {
		out = append(out, r.apply(mod, opts)...)
	}
	return out
}
