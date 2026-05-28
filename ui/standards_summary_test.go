// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"testing"

	"git.alexander4.io/vigo/server/db"
)

// aggregateStandardsSnapshot is the pure folder that consumes a single
// snapshot date's per-framework rows. These tests pin its invariants
// without a real database.

// TestAggregateStandardsSnapshot_UnweightedMean pins the ratified rule:
// overall compliance % on the dashboard / /compliance is the unweighted
// mean of framework scores, NOT a control-count-weighted sum. Regression
// guard against quietly slipping back to a weighted average.
func TestAggregateStandardsSnapshot_UnweightedMean(t *testing.T) {
	// Framework A: 5 controls, all satisfied → 100%
	// Framework B: 200 controls, 100 satisfied + 100 missing → 50%
	// Control-count-weighted average: (5*100 + 200*50) / 205 = 51.2%
	// Unweighted mean (ratified):     (100 + 50) / 2      = 75.0%
	rows := []db.ListComplianceSnapshotByDateRow{
		{Framework: "A", Satisfied: 5, NotSatisfied: 0, ScorePercent: 100},
		{Framework: "B", Satisfied: 100, NotSatisfied: 100, ScorePercent: 50},
	}
	s := aggregateStandardsSnapshot(rows)
	if s.Pct != 75.0 {
		t.Errorf("overview Pct = %v, want 75.0 (unweighted mean of 100 and 50)", s.Pct)
	}
}

// TestAggregateStandardsSnapshot_NoWaiverDoubleCount: the per-framework
// buckets are disjoint, so waivers never inflate Total beyond the
// framework's real control count. Regression guard for the historical
// bug where Satisfied already included waivers AND Waived was summed in.
func TestAggregateStandardsSnapshot_NoWaiverDoubleCount(t *testing.T) {
	rows := []db.ListComplianceSnapshotByDateRow{
		// 10 controls split disjoint: 7 satisfied + 2 waived + 1 missing.
		{Framework: "HIPAA", Satisfied: 7, Waived: 2, Partial: 0, NotSatisfied: 1, ScorePercent: 70},
	}
	s := aggregateStandardsSnapshot(rows)
	if s.Total != 10 {
		t.Errorf("Total = %d, want 10 (framework has 10 controls; waivers must not inflate Total)", s.Total)
	}
	if s.Satisfied != 7 {
		t.Errorf("Satisfied = %d, want 7", s.Satisfied)
	}
	if s.Waived != 2 {
		t.Errorf("Waived = %d, want 2", s.Waived)
	}
	if s.Pct != 70.0 {
		t.Errorf("Pct = %v, want 70.0 (= framework's ScorePercent, unweighted mean of one framework)", s.Pct)
	}
}

// TestAggregateStandardsSnapshot_EmptyRows guards against divide-by-zero
// on a fresh server before the first snapshot lands.
func TestAggregateStandardsSnapshot_EmptyRows(t *testing.T) {
	s := aggregateStandardsSnapshot(nil)
	if s.Pct != 0 || s.Total != 0 {
		t.Errorf("empty rows: Pct=%v Total=%d, want both 0", s.Pct, s.Total)
	}
}

// TestAggregateStandardsSnapshot_EmptyFrameworkSkipped: framework rows
// with zero controls (disabled or no data yet) are skipped — they don't
// drag the mean toward zero.
func TestAggregateStandardsSnapshot_EmptyFrameworkSkipped(t *testing.T) {
	rows := []db.ListComplianceSnapshotByDateRow{
		{Framework: "active", Satisfied: 8, NotSatisfied: 2, ScorePercent: 80},
		{Framework: "empty", Satisfied: 0, NotSatisfied: 0, ScorePercent: 0},
		{Framework: "also-active", Satisfied: 6, NotSatisfied: 4, ScorePercent: 60},
	}
	s := aggregateStandardsSnapshot(rows)
	if s.Pct != 70.0 {
		t.Errorf("Pct = %v, want 70.0 (mean of 80 and 60; empty framework ignored)", s.Pct)
	}
	if s.Total != 20 {
		t.Errorf("Total = %d, want 20 (10 + 10 + 0)", s.Total)
	}
}
