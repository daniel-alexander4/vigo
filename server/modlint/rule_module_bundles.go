// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/compliance"
)

// moduleBundleRule validates a module's `bundle:` list against the
// Vigo-curated catalog at server/compliance/bundles.go and flags
// redundant declarations where the same (framework, control) pair is
// also listed in the module's explicit `compliance:` block. Sibling
// rule to moduleProvisionsRule — provisions and bundles split into two
// catalogs intentionally; the modlint surface mirrors the split so
// operators see the same error shape regardless of which catalog
// rejected their tag.
//
// Unknown bundle tag → Error with did-you-mean. The loader treats
// unknowns as zero contribution (ResolveBundle returns nil), so an
// unchecked typo silently de-tags the module from a framework section.
// Error severity catches that at publish time.
//
// Redundant declaration → Warning. Block isn't broken — explicit claims
// remain valid — but the noise hides the operator's actual intent.
type moduleBundleRule struct{}

func (moduleBundleRule) id() string { return "module-bundle" }

func (moduleBundleRule) apply(mod *module, _ Options) []Finding {
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
				RuleID:   "module-bundle",
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
				RuleID:   "module-bundle",
				Message: fmt.Sprintf(
					"bundle %q is not in the catalog. Run `vigocli compliance bundles` to list known tags.",
					b,
				),
			})
		}
	}

	if len(mod.Compliance) > 0 {
		bundled := make(map[string]map[string]string)
		for _, b := range mod.Bundle {
			if !knownSet[b] {
				continue
			}
			for framework, controls := range compliance.ResolveBundle(b) {
				if bundled[framework] == nil {
					bundled[framework] = make(map[string]string)
				}
				for _, cid := range controls {
					bundled[framework][cid] = b
				}
			}
		}
		type dup struct{ framework, control, bundle string }
		var dups []dup
		for framework, controls := range mod.Compliance {
			byCID, ok := bundled[framework]
			if !ok {
				continue
			}
			for _, cid := range controls {
				if b, hit := byCID[cid]; hit {
					dups = append(dups, dup{framework, cid, b})
				}
			}
		}
		sort.Slice(dups, func(i, j int) bool {
			if dups[i].framework != dups[j].framework {
				return dups[i].framework < dups[j].framework
			}
			return dups[i].control < dups[j].control
		})
		for _, d := range dups {
			out = append(out, Finding{
				Severity: Warning,
				Line:     mod.Line,
				Column:   mod.Column,
				RuleID:   "module-bundle",
				Message: fmt.Sprintf(
					"redundant: compliance.%s[%q] is already covered by bundle:[%s] — drop it from the explicit block",
					d.framework, d.control, d.bundle,
				),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return strings.Compare(out[i].Message, out[j].Message) < 0
	})
	return out
}

func init() { register(moduleBundleRule{}) }
