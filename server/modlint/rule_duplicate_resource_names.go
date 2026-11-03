// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import "fmt"

// duplicateResourceNamesRule rejects a module with two resources that
// share a name. depends_on / notify / subscribes refer to resources by
// name; duplicates make those references ambiguous and the agent's
// behavior implementation-defined.
//
// The finding is anchored to the SECOND occurrence (the one that's
// shadowing) — that's the line the operator typically wants to see
// in their editor jump-to-error.
type duplicateResourceNamesRule struct{}

func (duplicateResourceNamesRule) id() string { return "duplicate-resource-names" }

func (duplicateResourceNamesRule) apply(mod *configcrate, _ Options) []Finding {
	seen := make(map[string]bool, len(mod.Resources))
	var out []Finding
	for _, res := range mod.Resources {
		if res.Name == "" {
			continue
		}
		if seen[res.Name] {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "duplicate-resource-names",
				Message:  fmt.Sprintf("duplicate resource name %q", res.Name),
			})
		}
		seen[res.Name] = true
	}
	return out
}

func init() { register(duplicateResourceNamesRule{}) }
