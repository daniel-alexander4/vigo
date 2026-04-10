// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"path/filepath"
	"strings"
)

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
