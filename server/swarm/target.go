// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package swarm

import (
	"path/filepath"
	"strings"
)

// IsClaimOnlyLabel reports whether a swarm blob label is routed
// through a per-subsystem claim table instead of the standard
// target-pattern fan-out. Today the only claim-only namespace is
// gitback — content seeds are published with target "*" so the
// swarm layer distributes them freely, but the actual per-envoy
// mirror set is decided server-side by the gitback ClaimTable and
// delivered via CheckInResponse.gitback_claims.
//
// Status/UI code uses this predicate to bypass the wildcard pending
// fan-out for gitback blobs — otherwise envoys that are excluded
// by the sidecar's exclude_envoys/include_envoys/when filters would
// still show up as "pending" in swarm status output forever, even
// though they will never actually receive the blob.
//
// The set of claim-only prefixes must match
// agent/src/swarm/manifest.rs::is_claim_only_label. When adding a
// new claim-only namespace, update both sides in the same commit.
func IsClaimOnlyLabel(label string) bool {
	return strings.HasPrefix(label, "gitback:") || strings.HasPrefix(label, "gitback-meta:")
}

// MatchTarget checks whether an envoy matches a target spec. Supports
// comma-separated patterns with - prefix for exclusion:
//
//	"*"                  — all envoys
//	"*lap*"              — hostname glob
//	"*lap*,-rasp*"       — include *lap*, exclude rasp*
//	"os.distro=ubuntu"   — trait match
//
// An envoy matches if at least one inclusion pattern matches AND no
// exclusion pattern matches. If only exclusion patterns are given,
// all envoys are included by default (then exclusions applied).
func MatchTarget(hostname string, traits map[string]string, spec string) bool {
	parts := strings.Split(spec, ",")

	var includes, excludes []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "-") {
			excludes = append(excludes, p[1:])
		} else {
			includes = append(includes, p)
		}
	}

	// Check exclusions first — any match means reject.
	for _, pat := range excludes {
		if matchSingleTarget(hostname, traits, pat) {
			return false
		}
	}

	// If no inclusion patterns, default to include-all (exclusions-only mode).
	if len(includes) == 0 {
		return true
	}

	// At least one inclusion pattern must match.
	for _, pat := range includes {
		if matchSingleTarget(hostname, traits, pat) {
			return true
		}
	}
	return false
}

// matchSingleTarget matches a single pattern (glob, trait=value, or exact hostname).
func matchSingleTarget(hostname string, traits map[string]string, pat string) bool {
	if pat == "*" {
		return true
	}
	if strings.Contains(pat, "=") {
		parts := strings.SplitN(pat, "=", 2)
		if len(parts) == 2 {
			return traits[parts[0]] == parts[1]
		}
	}
	if matched, _ := filepath.Match(pat, hostname); matched {
		return true
	}
	return hostname == pat
}
