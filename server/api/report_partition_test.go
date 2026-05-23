// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"testing"

	"git.alexander4.io/vigo/server/config"
)

// TestPartitionEnvoyFindings pins the single-source-of-truth partition
// priority used by both the framework fleet-row and the envoy-detail
// card. Priority: module-tagged → Connwaer-trait satisfied → waived →
// partial → missing. Framework reports score envoy state only — Vigo's
// own product-level posture never inflates these counts.
func TestPartitionEnvoyFindings(t *testing.T) {
	findings := []ControlFinding{
		{ControlID: "c-mod", Status: "not_satisfied"},  // covered via module tag
		{ControlID: "c-connwaer", Status: "satisfied"}, // Connwaer trait escalation — no module, credit by status
		{ControlID: "c-waived", Status: "waived"},      // waiver applied
		{ControlID: "c-partial", Status: "partial"},    // framework half-credit
		{ControlID: "c-miss", Status: "not_satisfied"}, // plain missing
		{ControlID: "c-dual", Status: "waived"},        // both module tag AND waived — module wins
	}
	coverage := &config.StandardCoverage{
		CoveredControls: []config.ControlCoverage{
			{ControlID: "c-mod", Modules: []string{"mod-a"}},
			{ControlID: "c-dual", Modules: []string{"mod-b"}},
		},
	}

	covered, waived, partial, missing := partitionEnvoyFindings(findings, coverage, nil)

	if covered != 3 { // c-mod + c-connwaer + c-dual
		t.Errorf("covered = %d, want 3", covered)
	}
	if waived != 1 { // c-waived (c-dual would have been waived but module wins)
		t.Errorf("waived = %d, want 1", waived)
	}
	if partial != 1 { // c-partial
		t.Errorf("partial = %d, want 1", partial)
	}
	if missing != 1 { // c-miss
		t.Errorf("missing = %d, want 1", missing)
	}
	if total := covered + waived + partial + missing; total != len(findings) {
		t.Errorf("buckets sum to %d, want %d (every finding lands in exactly one bucket)", total, len(findings))
	}
}

// TestPartitionEnvoyFindingsNilCoverage verifies that an envoy with no
// module coverage still gets Connwaer-trait escalations and waivers
// credited — only module-tag-based covered counts fall to zero.
func TestPartitionEnvoyFindingsNilCoverage(t *testing.T) {
	findings := []ControlFinding{
		{ControlID: "c-connwaer", Status: "satisfied"},
		{ControlID: "c-waived", Status: "waived"},
		{ControlID: "c-miss", Status: "not_satisfied"},
	}

	covered, waived, partial, missing := partitionEnvoyFindings(findings, nil, nil)

	if covered != 1 { // c-connwaer (Connwaer trait escalation)
		t.Errorf("covered = %d, want 1 (Connwaer trait escalation)", covered)
	}
	if waived != 1 {
		t.Errorf("waived = %d, want 1", waived)
	}
	if partial != 0 {
		t.Errorf("partial = %d, want 0", partial)
	}
	if missing != 1 {
		t.Errorf("missing = %d, want 1", missing)
	}
}

// TestPartitionEnvoyFindingsEnvoyOnly is the regression guard for the
// 0.32.0 cleanup: framework builders no longer hardcode any control as
// satisfied. An envoy with zero modules and no waivers must report
// every catalog entry as missing — not inflated by Vigo's own
// architectural posture.
func TestPartitionEnvoyFindingsEnvoyOnly(t *testing.T) {
	findings := []ControlFinding{
		{ControlID: "a", Status: "not_satisfied"},
		{ControlID: "b", Status: "not_satisfied"},
		{ControlID: "c", Status: "not_satisfied"},
	}

	covered, waived, partial, missing := partitionEnvoyFindings(findings, nil, nil)

	if covered != 0 {
		t.Errorf("covered = %d, want 0 — Vigo's own posture must not inflate envoy scoring", covered)
	}
	if waived != 0 || partial != 0 {
		t.Errorf("waived=%d partial=%d, want 0 0 — only modules / Connwaer / waivers move controls out of missing", waived, partial)
	}
	if missing != len(findings) {
		t.Errorf("missing = %d, want %d (every catalog entry uncovered when no envoy state)", missing, len(findings))
	}
}
