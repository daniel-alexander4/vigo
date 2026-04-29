// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LintWaiversFile validates a waivers.vgo (compliance waivers).
//
// Each waiver entry has:
//   - control: required, in the form "framework/control_id" where
//     framework is a canonical slug (hipaa, soc2, pci-dss, ...).
//     This rule canonicalizes the framework prefix the same way
//     compliance-framework canonicalizes module-level keys, and
//     auto-fixes "HIPAA/164.312(a)(1)" to "hipaa/164.312(a)(1)".
//   - reason: required, non-empty.
//   - approved_by: required, non-empty.
//   - expires: optional; if present, parses as YYYY-MM-DD. An expired
//     waiver is flagged as an Info finding so operators see they're
//     still claiming a waiver that no longer covers them.
//
// Cross-file resolution (does the framework/control_id pair exist in
// the catalog?) is Phase 6.
func LintWaiversFile(input []byte, _ Options) *Result {
	r := &Result{}
	repaired := repair(input, r)

	wf, err := parseWaiversFile(repaired)
	if err != nil {
		r.Findings = append(r.Findings, Finding{
			Severity: Error,
			RuleID:   "waiversfile-parse",
			Message:  fmt.Sprintf("waivers file parse error: %s", err),
		})
		finalizeFindings(r)
		return r
	}

	r.Findings = append(r.Findings, applyWaiversFileRules(wf, time.Now())...)
	r.Output = string(repaired)

	finalizeFindings(r)
	return r
}

type waiverEntry struct {
	Control    string
	Reason     string
	ApprovedBy string
	Expires    string
	Line       int
	Column     int
	// ControlNode points at the YAML scalar so a future writer pass
	// can rewrite the canonicalized framework slug in place. Unused
	// today (Phase 5 doesn't reformat).
	ControlNode *yaml.Node
}

type waiversFile struct {
	Waivers []waiverEntry
}

func parseWaiversFile(data []byte) (*waiversFile, error) {
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

	wf := &waiversFile{}
	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]
		if key != "waivers" || val.Kind != yaml.SequenceNode {
			continue
		}
		for _, blk := range val.Content {
			if blk.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("waivers[]: expected mapping")
			}
			w := waiverEntry{Line: blk.Line, Column: blk.Column}
			for j := 0; j < len(blk.Content)-1; j += 2 {
				k := blk.Content[j].Value
				v := blk.Content[j+1]
				switch k {
				case "control":
					w.Control = v.Value
					w.ControlNode = v
				case "reason":
					w.Reason = v.Value
				case "approved_by":
					w.ApprovedBy = v.Value
				case "expires":
					w.Expires = v.Value
				}
			}
			wf.Waivers = append(wf.Waivers, w)
		}
	}
	return wf, nil
}

// applyWaiversFileRules takes the parsed waivers and a `now` clock —
// passed in so tests can pin the current date when checking the
// expires field, rather than touching the wall clock.
func applyWaiversFileRules(wf *waiversFile, now time.Time) []Finding {
	var out []Finding
	for i, w := range wf.Waivers {
		// control: required, well-formed.
		if w.Control == "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     w.Line,
				Column:   w.Column,
				RuleID:   "waiversfile-control-required",
				Message:  fmt.Sprintf("waivers[%d]: 'control' is required", i),
			})
		} else if !strings.Contains(w.Control, "/") {
			out = append(out, Finding{
				Severity: Error,
				Line:     w.Line,
				Column:   w.Column,
				RuleID:   "waiversfile-control-shape",
				Message: fmt.Sprintf(
					"waivers[%d]: control %q must be in the form 'framework/control_id' (e.g., 'hipaa/164.312(a)(1)')",
					i, w.Control,
				),
			})
		} else {
			// Framework prefix canonicalization. Auto-fix only if the
			// canonical form matches a known framework — unknown
			// frameworks get a did-you-mean Error.
			fw, ctrl := splitOnce(w.Control, "/")
			canon := canonicalize(fw)
			if canon != fw && canonicalFrameworkSet[canon] {
				newControl := canon + "/" + ctrl
				wf.Waivers[i].Control = newControl
				if w.ControlNode != nil {
					w.ControlNode.Value = newControl
				}
				out = append(out, Finding{
					Severity:   Warning,
					Line:       w.Line,
					Column:     w.Column,
					RuleID:     "waiversfile-framework-canonicalize",
					FixApplied: true,
					Message: fmt.Sprintf(
						"waivers[%d]: framework prefix %q normalized to %q",
						i, fw, canon,
					),
				})
			} else if !canonicalFrameworkSet[canon] {
				if guess := closestKey(canon, canonicalFrameworkSlugs); guess != "" {
					out = append(out, Finding{
						Severity: Error,
						Line:     w.Line,
						Column:   w.Column,
						RuleID:   "waiversfile-framework-unknown",
						Message: fmt.Sprintf(
							"waivers[%d]: framework %q is not recognized — did you mean %q?",
							i, fw, guess,
						),
					})
				} else {
					out = append(out, Finding{
						Severity: Error,
						Line:     w.Line,
						Column:   w.Column,
						RuleID:   "waiversfile-framework-unknown",
						Message: fmt.Sprintf(
							"waivers[%d]: framework %q is not recognized",
							i, fw,
						),
					})
				}
			}
		}

		// reason: non-empty required. A waiver without a reason is
		// undocumented compliance debt.
		if strings.TrimSpace(w.Reason) == "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     w.Line,
				Column:   w.Column,
				RuleID:   "waiversfile-reason-required",
				Message:  fmt.Sprintf("waivers[%d]: 'reason' is required and must be non-empty", i),
			})
		}

		// approved_by: non-empty required. Audit trail.
		if strings.TrimSpace(w.ApprovedBy) == "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     w.Line,
				Column:   w.Column,
				RuleID:   "waiversfile-approved-by-required",
				Message:  fmt.Sprintf("waivers[%d]: 'approved_by' is required and must be non-empty", i),
			})
		}

		// expires: optional, but if set must parse and is flagged as
		// info if already past.
		if w.Expires != "" {
			t, err := time.Parse("2006-01-02", w.Expires)
			if err != nil {
				out = append(out, Finding{
					Severity: Error,
					Line:     w.Line,
					Column:   w.Column,
					RuleID:   "waiversfile-expires-format",
					Message: fmt.Sprintf(
						"waivers[%d]: expires %q must be YYYY-MM-DD format",
						i, w.Expires,
					),
				})
			} else if t.Before(now) {
				out = append(out, Finding{
					Severity: Info,
					Line:     w.Line,
					Column:   w.Column,
					RuleID:   "waiversfile-expired",
					Message: fmt.Sprintf(
						"waivers[%d]: expired on %s — this control is no longer covered by the waiver",
						i, w.Expires,
					),
				})
			}
		}
	}
	return out
}

// splitOnce splits s on the first occurrence of sep. If sep is not
// found, returns (s, ""). Avoids importing strings.SplitN for one
// usage.
func splitOnce(s, sep string) (string, string) {
	if idx := strings.Index(s, sep); idx >= 0 {
		return s[:idx], s[idx+len(sep):]
	}
	return s, ""
}
