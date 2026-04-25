// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"testing"
)

// standardsFromReport takes `any` and unmarshals it as an Executive report.
// Feeding it a plain map lets us pin the aggregator math without standing up
// the full server-side report builder.
type execReportFixture struct {
	Score struct {
		Percent float64 `json:"percent"`
	} `json:"score"`
	Frameworks []frameworkFixture `json:"frameworks"`
}

type frameworkFixture struct {
	Name         string  `json:"name"`
	Satisfied    int     `json:"satisfied"`
	Waived       int     `json:"waived"`
	Partial      int     `json:"partial"`
	NotSatisfied int     `json:"not_satisfied"`
	ScorePercent float64 `json:"score_percent"`
}

// TestStandardsFromReport_UnweightedMean pins the ratified rule: overall
// compliance % on the /compliance overview is the unweighted mean of
// framework scores, NOT a control-count-weighted sum. Regression guard
// for bug #3 in the audit.
func TestStandardsFromReport_UnweightedMean(t *testing.T) {
	h := &Handler{}
	// Two frameworks, very different control counts.
	// Framework A: 5 controls, all satisfied → 100%
	// Framework B: 200 controls, 100 satisfied + 100 missing → 50%
	// Control-count-weighted average: (5*100 + 200*50) / 205 = 51.2%
	// Unweighted mean (ratified):     (100 + 50) / 2      = 75.0%
	report := execReportFixture{
		Frameworks: []frameworkFixture{
			{Name: "A", Satisfied: 5, NotSatisfied: 0, ScorePercent: 100},
			{Name: "B", Satisfied: 100, NotSatisfied: 100, ScorePercent: 50},
		},
	}
	s := h.standardsFromReport(report)
	if s.Pct != 75.0 {
		t.Errorf("overview Pct = %v, want 75.0 (unweighted mean of 100 and 50)", s.Pct)
	}
}

// TestStandardsFromReport_NoWaiverDoubleCount is the specific regression
// guard for bug #4: the old implementation summed every framework's
// Satisfied+Waived+Partial+NotSatisfied into Total, where Satisfied already
// included waivers — so each waiver inflated Total by the quantity "1
// control" beyond the framework's real control count. The new aggregator
// keeps disjoint buckets and computes % via the unweighted mean of
// framework scores, so waivers cannot pollute the overview percentage.
func TestStandardsFromReport_NoWaiverDoubleCount(t *testing.T) {
	h := &Handler{}
	// Framework of 10 controls with 7 satisfied + 2 waived + 1 missing
	// (disjoint buckets from the unified scorer).
	report := execReportFixture{
		Frameworks: []frameworkFixture{
			{Name: "HIPAA", Satisfied: 7, Waived: 2, Partial: 0, NotSatisfied: 1, ScorePercent: 70},
		},
	}
	s := h.standardsFromReport(report)
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

// TestStandardsFromReport_EmptyFrameworks ensures we don't divide by zero
// when no frameworks have any controls loaded.
func TestStandardsFromReport_EmptyFrameworks(t *testing.T) {
	h := &Handler{}
	report := execReportFixture{
		Frameworks: []frameworkFixture{
			{Name: "empty", Satisfied: 0, Waived: 0, Partial: 0, NotSatisfied: 0},
		},
	}
	s := h.standardsFromReport(report)
	if s.Pct != 0 || s.Total != 0 {
		t.Errorf("empty frameworks: Pct=%v Total=%d, want both 0", s.Pct, s.Total)
	}
}

// TestStandardsFromReport_MixedEnablement verifies that disabled-or-empty
// frameworks (total == 0) are excluded from both the count sums and the
// mean-of-percentages calculation.
func TestStandardsFromReport_MixedEnablement(t *testing.T) {
	h := &Handler{}
	report := execReportFixture{
		Frameworks: []frameworkFixture{
			{Name: "active", Satisfied: 8, NotSatisfied: 2, ScorePercent: 80},
			{Name: "empty", Satisfied: 0, NotSatisfied: 0, ScorePercent: 0}, // no data — should be ignored
			{Name: "also-active", Satisfied: 6, NotSatisfied: 4, ScorePercent: 60},
		},
	}
	s := h.standardsFromReport(report)
	if s.Pct != 70.0 {
		t.Errorf("Pct = %v, want 70.0 (mean of 80 and 60; empty framework ignored)", s.Pct)
	}
	if s.Total != 20 {
		t.Errorf("Total = %d, want 20 (10 + 10 + 0)", s.Total)
	}
}
