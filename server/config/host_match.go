// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"path/filepath"
)

// HostMatchesPatterns reports whether `hostname` is allowed by the swarm
// authorization pattern list. The semantics are intentionally identical
// to Vigo's hostcrate rules (ADR-002):
//
//   - Empty list  → false (default-deny when nothing is configured).
//   - First match wins. Patterns evaluated in order; first hit decides.
//   - "-" prefix  → deny pattern. Remainder is a filepath.Match glob;
//     match means the host is denied.
//   - Otherwise   → allow pattern. The pattern is a filepath.Match glob;
//     match means the host is allowed.
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
// Malformed patterns are rejected when server.yaml loads
// (ValidatePatternList). Should one reach this function anyway, it fails
// closed: a malformed deny pattern denies the host, and a malformed allow
// pattern never matches — a typo can never let a host through that the
// operator meant to deny.
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
		matched, err := filepath.Match(glob, hostname)
		if err != nil {
			if deny {
				return false
			}
			continue
		}
		if matched {
			return !deny
		}
	}
	return false
}

// ValidatePatternList returns an error naming the first malformed glob in a
// hostcrate-semantics pattern list (a `-` deny prefix is stripped first).
func ValidatePatternList(name string, patterns []string) error {
	for i, pat := range patterns {
		glob := pat
		if len(glob) > 0 && glob[0] == '-' {
			glob = glob[1:]
		}
		if glob == "" {
			continue
		}
		if _, err := filepath.Match(glob, ""); err != nil {
			return fmt.Errorf("%s[%d]: %q is not a valid hostname glob: %w", name, i, pat, err)
		}
	}
	return nil
}
