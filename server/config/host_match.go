// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import "path/filepath"

// HostMatchesPatterns reports whether `hostname` is allowed by the swarm
// authorization pattern list. The semantics are intentionally identical
// to Vigo's matchfile rules (ADR-002):
//
//   - Empty list  → false (default-deny when nothing is configured).
//   - First match wins. Patterns evaluated in order; first hit decides.
//   - "-" prefix  → deny pattern. Remainder is a filepath.Match glob;
//                   match means the host is denied.
//   - Otherwise   → allow pattern. The pattern is a filepath.Match glob;
//                   match means the host is allowed.
//   - No match    → false (default-deny on no match).
//
// Examples:
//
//	[]                                    → all hosts denied (subsystem off)
//	["*"]                                 → all hosts allowed (fleet-wide on)
//	["-rasp1", "*"]                       → everything except rasp1
//	["annlap", "plex"]                    → only those two; everything else denied
//	["-*.test.example.com", "*"]          → all production envoys, no test
//
// Malformed patterns (filepath.Match returns ErrBadPattern) are skipped
// silently so a single typo doesn't take down the whole list.
func HostMatchesPatterns(hostname string, patterns []string) bool {
	for _, pat := range patterns {
		if pat == "" {
			continue
		}
		deny := false
		glob := pat
		if pat[0] == '-' {
			deny = true
			glob = pat[1:]
		}
		if glob == "" {
			continue
		}
		if matched, err := filepath.Match(glob, hostname); err == nil && matched {
			return !deny
		}
	}
	return false
}
