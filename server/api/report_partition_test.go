// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"testing"

	"github.com/daniel-alexander4/vigo/server/config"
)

// TestPartitionEnvoyFindings pins the single-source-of-truth partition
// priority used by both the framework fleet-row and the envoy-detail card.
// Priority: module-tagged → architectural satisfaction → waived → partial → missing.
func TestPartitionEnvoyFindings(t *testing.T) {
	findings := []ControlFinding{
		{ControlID: "c-mod", Status: "not_satisfied"},   // covered via module tag
		{ControlID: "c-arch", Status: "satisfied"},      // architectural — no module, credit by status
		{ControlID: "c-waived", Status: "waived"},       // waiver applied
		{ControlID: "c-partial", Status: "partial"},     // framework half-credit
		{ControlID: "c-miss", Status: "not_satisfied"},  // plain missing
		{ControlID: "c-dual", Status: "waived"},         // both module tag AND waived — module wins
	}
	coverage := &config.StandardCoverage{
		CoveredControls: []config.ControlCoverage{
			{ControlID: "c-mod", Modules: []string{"mod-a"}},
			{ControlID: "c-dual", Modules: []string{"mod-b"}},
		},
	}

	covered, waived, partial, missing := partitionEnvoyFindings(findings, coverage)

	if covered != 3 { // c-mod + c-arch + c-dual
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
// module coverage still gets architectural satisfactions and waivers
// credited — only module-tag-based covered counts fall to zero.
func TestPartitionEnvoyFindingsNilCoverage(t *testing.T) {
	findings := []ControlFinding{
		{ControlID: "c-arch", Status: "satisfied"},
		{ControlID: "c-waived", Status: "waived"},
		{ControlID: "c-miss", Status: "not_satisfied"},
	}

	covered, waived, partial, missing := partitionEnvoyFindings(findings, nil)

	if covered != 1 { // c-arch still gets architectural credit
		t.Errorf("covered = %d, want 1 (architectural satisfaction)", covered)
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
