// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"testing"

	"git.alexander4.io/vigo/ui"
)

// TestAggregateFleet_EmptyFleet pins the zero-envoy edge case — no
// division-by-zero, no NaN, all counts zero, CoveragePercent zero.
func TestAggregateFleet_EmptyFleet(t *testing.T) {
	agg := aggregateFleetPartitions(nil, 10)
	if agg.ActiveEnvoys != 0 || agg.Covered != 0 || agg.Waived != 0 ||
		agg.Partial != 0 || agg.Missing != 0 || agg.CoveragePercent != 0 {
		t.Fatalf("empty fleet aggregate nonzero: %+v", agg)
	}
}

// TestAggregateFleet_ZeroTotal covers a framework with no findings.
func TestAggregateFleet_ZeroTotal(t *testing.T) {
	perEnvoy := map[string]ui.EnvoyFrameworkStatus{
		"e1": {Total: 0},
	}
	agg := aggregateFleetPartitions(perEnvoy, 0)
	if agg.CoveragePercent != 0 {
		t.Errorf("zero-total CoveragePercent = %v, want 0", agg.CoveragePercent)
	}
	if agg.Missing != 0 {
		t.Errorf("zero-total Missing = %d, want 0", agg.Missing)
	}
}

// TestAggregateFleet_Homogeneous verifies that a fleet where every envoy
// has identical buckets aggregates to exactly those buckets.
func TestAggregateFleet_Homogeneous(t *testing.T) {
	perEnvoy := map[string]ui.EnvoyFrameworkStatus{
		"e1": {Covered: 7, Waived: 1, Partial: 1, Missing: 1, Total: 10, CoveragePercent: 70},
		"e2": {Covered: 7, Waived: 1, Partial: 1, Missing: 1, Total: 10, CoveragePercent: 70},
		"e3": {Covered: 7, Waived: 1, Partial: 1, Missing: 1, Total: 10, CoveragePercent: 70},
	}
	agg := aggregateFleetPartitions(perEnvoy, 10)

	if agg.Covered != 7 || agg.Waived != 1 || agg.Partial != 1 || agg.Missing != 1 {
		t.Errorf("homogeneous aggregate drifted from per-envoy: %+v", agg)
	}
	if agg.CoveragePercent != 70 {
		t.Errorf("homogeneous percent = %v, want 70", agg.CoveragePercent)
	}
	if sum := agg.Covered + agg.Waived + agg.Partial + agg.Missing; sum != agg.Total {
		t.Errorf("balance invariant broken: %d+%d+%d+%d = %d, want %d",
			agg.Covered, agg.Waived, agg.Partial, agg.Missing, sum, agg.Total)
	}
}

// TestAggregateFleet_BalanceInvariant is the regression guard for bug #1
// in the compliance audit — the per-framework summary card was macro-
// averaging percentages and then *synthesizing* Satisfied from avg%×Total
// with Partial forced to zero, so the top card didn't sum to Total and
// didn't match the fleet rows. This test pins the invariant that every
// aggregate, on any heterogeneous fleet, sums to Total.
func TestAggregateFleet_BalanceInvariant(t *testing.T) {
	cases := []struct {
		name     string
		perEnvoy map[string]ui.EnvoyFrameworkStatus
		total    int
	}{
		{
			name: "heterogeneous small fleet",
			perEnvoy: map[string]ui.EnvoyFrameworkStatus{
				"e1": {Covered: 8, Waived: 0, Partial: 0, Missing: 2, Total: 10, CoveragePercent: 80},
				"e2": {Covered: 4, Waived: 2, Partial: 1, Missing: 3, Total: 10, CoveragePercent: 40},
				"e3": {Covered: 10, Waived: 0, Partial: 0, Missing: 0, Total: 10, CoveragePercent: 100},
			},
			total: 10,
		},
		{
			name: "fractional mean forces rounding",
			perEnvoy: map[string]ui.EnvoyFrameworkStatus{
				"e1": {Covered: 5, Waived: 0, Partial: 0, Missing: 5, Total: 10, CoveragePercent: 50},
				"e2": {Covered: 6, Waived: 0, Partial: 0, Missing: 4, Total: 10, CoveragePercent: 60},
			},
			total: 10,
		},
		{
			name: "partial non-zero — regression guard, used to be forced 0",
			perEnvoy: map[string]ui.EnvoyFrameworkStatus{
				"e1": {Covered: 3, Waived: 1, Partial: 4, Missing: 2, Total: 10, CoveragePercent: 30},
				"e2": {Covered: 4, Waived: 0, Partial: 3, Missing: 3, Total: 10, CoveragePercent: 40},
			},
			total: 10,
		},
		{
			name: "waiver-heavy fleet",
			perEnvoy: map[string]ui.EnvoyFrameworkStatus{
				"e1": {Covered: 2, Waived: 5, Partial: 0, Missing: 3, Total: 10, CoveragePercent: 20},
				"e2": {Covered: 3, Waived: 4, Partial: 0, Missing: 3, Total: 10, CoveragePercent: 30},
				"e3": {Covered: 2, Waived: 6, Partial: 0, Missing: 2, Total: 10, CoveragePercent: 20},
			},
			total: 10,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := aggregateFleetPartitions(tc.perEnvoy, tc.total)
			sum := agg.Covered + agg.Waived + agg.Partial + agg.Missing
			if sum != agg.Total {
				t.Errorf("balance invariant broken: C=%d W=%d P=%d M=%d sum=%d want=%d (perEnvoy=%v)",
					agg.Covered, agg.Waived, agg.Partial, agg.Missing, sum, agg.Total, tc.perEnvoy)
			}
			if agg.Covered < 0 || agg.Waived < 0 || agg.Partial < 0 || agg.Missing < 0 {
				t.Errorf("negative bucket: %+v", agg)
			}
			if agg.CoveragePercent < 0 || agg.CoveragePercent > 100 {
				t.Errorf("CoveragePercent outside [0,100]: %v", agg.CoveragePercent)
			}
		})
	}
}

// TestAggregateFleet_PartialIsReal is the specific regression guard for
// bug #1 in the audit: before the refactor, the per-framework summary card
// had `Partial = 0` hard-coded regardless of per-envoy partial counts.
// After the refactor, Partial is the rounded mean of per-envoy partials.
func TestAggregateFleet_PartialIsReal(t *testing.T) {
	perEnvoy := map[string]ui.EnvoyFrameworkStatus{
		"e1": {Covered: 6, Waived: 0, Partial: 2, Missing: 2, Total: 10, CoveragePercent: 60},
		"e2": {Covered: 6, Waived: 0, Partial: 2, Missing: 2, Total: 10, CoveragePercent: 60},
	}
	agg := aggregateFleetPartitions(perEnvoy, 10)
	if agg.Partial != 2 {
		t.Fatalf("Partial = %d, want 2 (the per-envoy mean; pre-refactor this was forced to 0)", agg.Partial)
	}
}

// TestAggregateFleet_WaivedNotDoubleCounted is the regression guard for
// bug #4 in the audit: the overview page's standardsFromReport summed
// Satisfied + Waived + Partial + NotSatisfied, where Satisfied already
// included waived controls and Waived was a raw waiver count — so any
// fleet with waivers inflated Total past the framework size and got a
// wrong percentage. The new aggregator keeps buckets disjoint: Covered,
// Waived, Partial, Missing each count distinct controls.
func TestAggregateFleet_WaivedNotDoubleCounted(t *testing.T) {
	perEnvoy := map[string]ui.EnvoyFrameworkStatus{
		"e1": {Covered: 5, Waived: 3, Partial: 0, Missing: 2, Total: 10, CoveragePercent: 50},
	}
	agg := aggregateFleetPartitions(perEnvoy, 10)
	if sum := agg.Covered + agg.Waived + agg.Partial + agg.Missing; sum != 10 {
		t.Fatalf("buckets sum to %d, want 10 — a waiver must NOT be counted both inside Covered and again in its own bucket", sum)
	}
	if agg.Covered != 5 {
		t.Errorf("Covered = %d, want 5 (must not fold waived controls)", agg.Covered)
	}
	if agg.Waived != 3 {
		t.Errorf("Waived = %d, want 3", agg.Waived)
	}
}

// TestAggregateFleet_RoundingOvershoot pins behavior at the rounding
// boundary where the sum of rounded means would exceed Total. The
// aggregator trims from the largest bucket so Missing stays ≥ 0.
func TestAggregateFleet_RoundingOvershoot(t *testing.T) {
	// With mean(Covered)=4.5, mean(Waived)=4.5, mean(Partial)=1.5, round-half-to-even
	// gives 4, 4, 2 — no overshoot. Construct a case that definitely overshoots.
	perEnvoy := map[string]ui.EnvoyFrameworkStatus{
		"e1": {Covered: 4, Waived: 5, Partial: 1, Missing: 0, Total: 10, CoveragePercent: 40},
		"e2": {Covered: 5, Waived: 4, Partial: 1, Missing: 0, Total: 10, CoveragePercent: 50},
	}
	agg := aggregateFleetPartitions(perEnvoy, 10)
	if agg.Missing < 0 {
		t.Errorf("Missing went negative despite overshoot trim: %+v", agg)
	}
	if sum := agg.Covered + agg.Waived + agg.Partial + agg.Missing; sum != 10 {
		t.Errorf("overshoot case did not balance: C=%d W=%d P=%d M=%d sum=%d", agg.Covered, agg.Waived, agg.Partial, agg.Missing, sum)
	}
}
