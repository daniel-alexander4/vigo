// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import "testing"

// TestIsClaimOnlyLabel_VigorepoNamespaces pins the set of label
// prefixes that are routed through per-subsystem claim tables rather
// than the standard wildcard target fan-out. Must stay in lockstep
// with agent/src/swarm/manifest.rs::is_claim_only_label — when new
// claim-only namespaces are added, update both sides in the same
// commit.
func TestIsClaimOnlyLabel_VigorepoNamespaces(t *testing.T) {
	claimOnly := []string{
		"vigorepo:host:user:proj",
		"vigorepo-meta:host:user:proj",
		"vigorepo:",      // edge: empty after prefix — still claim-only
		"vigorepo-meta:", // edge: empty after prefix — still claim-only
	}
	for _, l := range claimOnly {
		if !IsClaimOnlyLabel(l) {
			t.Errorf("expected %q to be claim-only", l)
		}
	}
}

// TestIsClaimOnlyLabel_StandardNamespaces verifies non-vigorepo labels
// fall through to the standard target-pattern fan-out path.
func TestIsClaimOnlyLabel_StandardNamespaces(t *testing.T) {
	standard := []string{
		"",
		"module-bundle",
		"Win11_25H2_English_x64.iso",
		"vigosync:user:file",
		"vigorepofoo",      // missing colon — not a namespace match
		"foo:vigorepo:bar", // not at start
		"VIGOREPO:X:Y:Z",   // case-sensitive, not claim-only
	}
	for _, l := range standard {
		if IsClaimOnlyLabel(l) {
			t.Errorf("expected %q to NOT be claim-only", l)
		}
	}
}
