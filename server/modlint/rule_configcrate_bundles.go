// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import (
	"fmt"
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/compliance"
)

// configcrateBundleRule validates a configcrate's `bundle:` list against the
// Vigo-curated catalog at server/compliance/bundles.go. Sibling rule to
// configcrateProvisionsRule — provisions and bundles split into two catalogs
// intentionally; the modlint surface mirrors the split so operators see the
// same error shape regardless of which catalog rejected their tag.
//
// Unknown bundle tag → Error with did-you-mean. The loader treats
// unknowns as zero contribution (ResolveBundle returns nil), so an
// unchecked typo silently de-tags the configcrate from a framework section.
// Error severity catches that at publish time.
type configcrateBundleRule struct{}

func (configcrateBundleRule) id() string { return "configcrate-bundle" }

func (configcrateBundleRule) apply(mod *configcrate, _ Options) []Finding {
	if len(mod.Bundle) == 0 {
		return nil
	}
	var out []Finding
	known := compliance.KnownBundleIDs()
	knownSet := make(map[string]bool, len(known))
	for _, id := range known {
		knownSet[id] = true
	}

	for _, b := range mod.Bundle {
		if knownSet[b] {
			continue
		}
		if guess := closestKey(b, known); guess != "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "configcrate-bundle",
				Message: fmt.Sprintf(
					"bundle %q is not in the catalog — did you mean %q? Run `vigocli compliance bundles` to list known tags.",
					b, guess,
				),
			})
		} else {
			out = append(out, Finding{
				Severity: Error,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "configcrate-bundle",
				Message: fmt.Sprintf(
					"bundle %q is not in the catalog. Run `vigocli compliance bundles` to list known tags.",
					b,
				),
			})
		}
	}

	// Redundancy check retired with the per-module `compliance:`
	// key in 0.66.48 — moduleComplianceKeyRetiredRule is now the
	// single error path for any `compliance:` block in a module YAML.
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Compare(out[i].Message, out[j].Message) < 0
	})
	return out
}

func init() { register(configcrateBundleRule{}) }
