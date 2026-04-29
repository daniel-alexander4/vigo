// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/conditionals"
	"gopkg.in/yaml.v3"
)

// LintMatchfile validates and normalizes a matchfile (any .vgo with
// an `envoys:` block — typically named envoys.vgo, but per-host
// filenames like web01.vgo work too). The pipeline mirrors Lint():
// repair → parse → rules → render. Output is the same shape as the
// input (matchfiles aren't reformatted today; the goal is to catch
// authoring errors before publish reaches the agent push path).
//
// Checks applied:
//   - Each match block has a `match:` value that is either a glob
//     pattern (filepath.Match) or a literal hostname.
//   - When-syntax across every block-level `when:` (reuses the same
//     conditionals parser as the module pipeline).
//   - Duplicate match patterns within the same file (the loader
//     keeps the first; subsequent blocks are silently shadowed).
//   - Reserved keys per block — unknown keys flagged as did-you-mean.
//   - `environment:` is a non-empty string when present.
func LintMatchfile(input []byte, _ Options) *Result {
	r := &Result{}
	repaired := repair(input, r)

	mf, err := parseMatchfile(repaired)
	if err != nil {
		r.Findings = append(r.Findings, Finding{
			Severity: Error,
			RuleID:   "matchfile-parse",
			Message:  fmt.Sprintf("matchfile parse error: %s", err),
		})
		finalizeFindings(r)
		return r
	}

	r.Findings = append(r.Findings, applyMatchfileRules(mf)...)
	r.Output = string(repaired) // structural lint only — no reformatting yet

	finalizeFindings(r)
	return r
}

// matchEntry is the parsed form of one envoys[] block, with line/col
// preserved for source-anchored findings.
type matchEntry struct {
	Match            string
	Environment      string
	When             string
	AttrKeys         []string // every key seen in the block, in source order
	Line             int
	Column           int
}

type matchfile struct {
	Entries []matchEntry
}

// matchfileKnownKeys is the canonical set of keys allowed inside an
// `envoys: - ...` block. Mirrors EnvoyEntry in server/config/types.go.
// Extending the loader requires adding the new key here too.
var matchfileKnownKeys = map[string]bool{
	"match": true, "environment": true, "roles": true, "modules": true,
	"vars": true, "tags": true, "exclude_modules": true,
	"exclude_usercrates": true, "exclude_resources": true,
	"observe_only": true, "when": true,
	"gateway": true, "device_type": true, "credential": true,
	"username": true, "port": true, "ip_address": true,
}

func parseMatchfile(data []byte) (*matchfile, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping at root")
	}

	mf := &matchfile{}
	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]
		if key != "envoys" {
			continue
		}
		if val.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("envoys: expected sequence")
		}
		for _, blk := range val.Content {
			if blk.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("envoys[]: expected mapping")
			}
			entry := matchEntry{Line: blk.Line, Column: blk.Column}
			for j := 0; j < len(blk.Content)-1; j += 2 {
				k := blk.Content[j].Value
				v := blk.Content[j+1]
				entry.AttrKeys = append(entry.AttrKeys, k)
				switch k {
				case "match":
					entry.Match = v.Value
				case "environment":
					entry.Environment = v.Value
				case "when":
					entry.When = v.Value
				}
			}
			mf.Entries = append(mf.Entries, entry)
		}
	}
	return mf, nil
}

func applyMatchfileRules(mf *matchfile) []Finding {
	var out []Finding

	// Track seen match patterns to flag duplicates.
	seenMatches := make(map[string]int) // pattern → first-seen index

	for i, e := range mf.Entries {
		// match: required, must be a valid filepath.Match pattern.
		if e.Match == "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     e.Line,
				Column:   e.Column,
				RuleID:   "matchfile-match-required",
				Message:  fmt.Sprintf("envoys[%d]: 'match' is required", i),
			})
		} else {
			// filepath.Match returns ErrBadPattern on invalid globs.
			// Probing with a dummy candidate exercises the pattern
			// parser without depending on what it actually matches.
			if _, err := filepath.Match(e.Match, "x"); err != nil {
				out = append(out, Finding{
					Severity: Error,
					Line:     e.Line,
					Column:   e.Column,
					RuleID:   "matchfile-match-syntax",
					Message: fmt.Sprintf(
						"envoys[%d]: match pattern %q is not a valid glob — %s",
						i, e.Match, err.Error(),
					),
				})
			}
			if firstIdx, dup := seenMatches[e.Match]; dup {
				out = append(out, Finding{
					Severity: Warning,
					Line:     e.Line,
					Column:   e.Column,
					RuleID:   "matchfile-duplicate-match",
					Message: fmt.Sprintf(
						"envoys[%d]: duplicate match pattern %q (first seen at envoys[%d]) — the loader keeps the first; this block is silently shadowed",
						i, e.Match, firstIdx,
					),
				})
			} else {
				seenMatches[e.Match] = i
			}
		}

		// environment: empty string is meaningless; either omit or set.
		if hasKey(e.AttrKeys, "environment") && strings.TrimSpace(e.Environment) == "" {
			out = append(out, Finding{
				Severity: Warning,
				Line:     e.Line,
				Column:   e.Column,
				RuleID:   "matchfile-environment-empty",
				Message: fmt.Sprintf(
					"envoys[%d]: 'environment' is set to empty — drop the key or give it a value",
					i,
				),
			})
		}

		// when: must parse if present.
		if e.When != "" {
			if _, err := conditionals.ParseWhenExpr(e.When); err != nil {
				out = append(out, Finding{
					Severity: Error,
					Line:     e.Line,
					Column:   e.Column,
					RuleID:   "matchfile-when-syntax",
					Message: fmt.Sprintf(
						"envoys[%d]: when: expression is malformed — %s",
						i, err.Error(),
					),
				})
			}
		}

		// Unknown keys → did-you-mean.
		for _, k := range e.AttrKeys {
			if matchfileKnownKeys[k] {
				continue
			}
			knownList := mapKeysSorted(matchfileKnownKeys)
			guess := closestKey(k, knownList)
			msg := fmt.Sprintf("envoys[%d]: unknown key %q", i, k)
			if guess != "" {
				msg += fmt.Sprintf(" — did you mean %q?", guess)
			}
			out = append(out, Finding{
				Severity: Error,
				Line:     e.Line,
				Column:   e.Column,
				RuleID:   "matchfile-unknown-key",
				Message:  msg,
			})
		}
	}
	return out
}

func hasKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func mapKeysSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Sort for deterministic did-you-mean ranking on ties.
	sortStringsAsc(out)
	return out
}

// sortStringsAsc avoids importing sort just for one call here. Tiny
// insertion sort — fine for our key-count scale (≤ 20).
func sortStringsAsc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
