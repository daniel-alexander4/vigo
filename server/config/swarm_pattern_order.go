// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SwarmPatternFinding describes a deny pattern in a `swarm.*.enabled`
// list that's unreachable behind an earlier non-deny entry — the
// silent-fail case `["*", "-rasp1"]` plus the glob-shadow case
// `["a*", "-annlap"]`. Surfaced as warnings (never errors) at config
// load + on the doctor endpoint so an operator gets a machine-checked
// nudge instead of relying on the docs paragraph alone.
type SwarmPatternFinding struct {
	// Subsystem is the friendly name of the pattern list — "swarm" for
	// the substrate-level list, or "puddle"/"gitmirror"/"filesync"/
	// "payload" for the per-subsystem lists.
	Subsystem string
	// Index is the zero-based position of the offending deny entry in
	// the list. Stable across reorders so the operator can find it.
	Index int
	// Pattern is the deny entry as written (with the leading `-`).
	Pattern string
	// Reason names the earlier pattern that shadows this deny and
	// explains why the deny is unreachable.
	Reason string
}

// String renders a finding as a one-line operator-facing message.
// Format is stable across versions for log-grep-ability.
func (f SwarmPatternFinding) String() string {
	return fmt.Sprintf(
		"swarm.%s.enabled[%d] = %q is unreachable: %s",
		f.Subsystem, f.Index, f.Pattern, f.Reason,
	)
}

// ValidateSwarmPatternOrder walks every pattern list in `s` (the
// substrate-level list plus each sub-subsystem) and flags deny
// entries shadowed by earlier non-deny patterns. Returns nil when
// every list is well-ordered. Pure function — no I/O, safe to call
// from any context.
//
// The check is conservative: a deny is only flagged when an earlier
// non-deny entry's `filepath.Match` glob matches the deny's target
// hostname literal. This catches both the obvious case (`["*",
// "-rasp1"]` — bare wildcard before deny) and the subtler glob
// case (`["a*", "-annlap"]` — overlap glob). It does NOT attempt
// to detect overlap between two glob patterns (e.g. `["a*", "-an*"]`)
// because the truth condition would require SAT-ish reasoning over
// regex-like globs, which is way out of scope for an operator nudge.
func ValidateSwarmPatternOrder(s SwarmSection) []SwarmPatternFinding {
	var out []SwarmPatternFinding
	out = append(out, checkPatternList("swarm", s.Enabled)...)
	out = append(out, checkPatternList("puddle", s.Puddle.Enabled)...)
	out = append(out, checkPatternList("gitmirror", s.Gitmirror.Enabled)...)
	out = append(out, checkPatternList("filesync", s.Filesync.Enabled)...)
	out = append(out, checkPatternList("payload", s.Payload.Enabled)...)
	return out
}

// checkPatternList walks one pattern list and returns at most one
// finding per shadowed deny entry. The operator-facing reason names
// the specific earlier pattern that shadows the deny, so they can
// fix it by reordering or dropping one of the two entries.
func checkPatternList(subsystem string, patterns []string) []SwarmPatternFinding {
	var out []SwarmPatternFinding
	for i, pat := range patterns {
		if !strings.HasPrefix(pat, "-") || len(pat) < 2 {
			continue
		}
		target := pat[1:]
		// Walk every preceding non-deny pattern. If any matches the
		// deny target via filepath.Match, this deny is unreachable.
		for j := 0; j < i; j++ {
			prev := patterns[j]
			if strings.HasPrefix(prev, "-") {
				continue // earlier deny doesn't shadow a later deny
			}
			matched, err := filepath.Match(prev, target)
			if err != nil || !matched {
				continue
			}
			var reason string
			if prev == "*" {
				reason = "preceded by bare \"*\" which matches every host"
			} else {
				reason = fmt.Sprintf(
					"preceded by %q which already matches %q via filepath.Match",
					prev, target,
				)
			}
			out = append(out, SwarmPatternFinding{
				Subsystem: subsystem,
				Index:     i,
				Pattern:   pat,
				Reason:    reason,
			})
			break // one finding per shadowed deny is enough
		}
	}
	return out
}
