// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleetpressure

import (
	"testing"
)

func newState(id, host string, traits map[string]string) EnvoySource {
	return EnvoySource{ID: id, Hostname: host, Traits: traits}
}

func TestComputeMissingTraitsAreUnknown(t *testing.T) {
	state := newState("e1", "h1", map[string]string{})
	p := Compute(state)
	if p.OpenFDs.Present {
		t.Errorf("expected open_fds Present=false; got Present=true value=%d", p.OpenFDs.Value)
	}
	if p.OpenFDs.Severity != Unknown {
		t.Errorf("expected open_fds Severity=Unknown; got %v", p.OpenFDs.Severity)
	}
	if p.Overall != OK {
		t.Errorf("expected Overall=OK with all missing; got %v", p.Overall)
	}
}

func TestComputeUnparseableTraitTreatedAsMissing(t *testing.T) {
	state := newState("e1", "h1", map[string]string{
		"agent_self.open_fds": "not-a-number",
	})
	p := Compute(state)
	if p.OpenFDs.Present {
		t.Errorf("garbage value should leave Present=false; got value=%d", p.OpenFDs.Value)
	}
	if p.OpenFDs.Severity != Unknown {
		t.Errorf("garbage value should classify Unknown; got %v", p.OpenFDs.Severity)
	}
}

func TestComputeOpenFDsThresholds(t *testing.T) {
	cases := []struct {
		raw      string
		want     Severity
		desc     string
	}{
		{"0", OK, "zero"},
		{"249", OK, "just below warn"},
		{"250", OK, "exactly warn boundary (>250 is warn, not >=)"},
		{"251", Warn, "just above warn"},
		{"500", Warn, "exactly fail boundary (>500 is fail, not >=)"},
		{"501", Fail, "just above fail"},
		{"99999", Fail, "way above fail"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			state := newState("e1", "h1", map[string]string{
				"agent_self.open_fds": tc.raw,
			})
			p := Compute(state)
			if p.OpenFDs.Severity != tc.want {
				t.Errorf("open_fds=%q: want %v, got %v", tc.raw, tc.want, p.OpenFDs.Severity)
			}
		})
	}
}

func TestComputeHostFDPctThresholds(t *testing.T) {
	cases := []struct {
		raw  string
		want Severity
	}{
		{"0.0", OK},
		{"49.5", OK},
		{"50.0", OK},
		{"50.01", Warn},
		{"79.9", Warn},
		{"80.0", Warn},
		{"80.5", Fail},
		{"99.9", Fail},
	}
	for _, tc := range cases {
		state := newState("e1", "h1", map[string]string{
			"host_self.fd_pct": tc.raw,
		})
		p := Compute(state)
		if p.FDPct.Severity != tc.want {
			t.Errorf("fd_pct=%q: want %v, got %v", tc.raw, tc.want, p.FDPct.Severity)
		}
	}
}

func TestComputeOomRecentTripsOnAnyNonzero(t *testing.T) {
	zero := newState("e1", "h1", map[string]string{"host_self.oom_recent": "0"})
	if got := Compute(zero).OomRecent.Severity; got != OK {
		t.Errorf("oom_recent=0 should be OK; got %v", got)
	}
	one := newState("e1", "h1", map[string]string{"host_self.oom_recent": "1"})
	if got := Compute(one).OomRecent.Severity; got != Warn {
		t.Errorf("oom_recent=1 should be Warn; got %v", got)
	}
	high := newState("e1", "h1", map[string]string{"host_self.oom_recent": "42"})
	if got := Compute(high).OomRecent.Severity; got != Warn {
		t.Errorf("oom_recent=42 should be Warn; got %v", got)
	}
}

func TestComputeOverallTakesWorst(t *testing.T) {
	state := newState("e1", "h1", map[string]string{
		"agent_self.open_fds":            "100",   // OK
		"agent_self.descendant_open_fds": "9999",  // Fail
		"host_self.fd_pct":               "10.0",  // OK
	})
	p := Compute(state)
	if p.Overall != Fail {
		t.Errorf("overall should be Fail (descendants in fail band); got %v", p.Overall)
	}
}

func TestComputeOverallIgnoresUnknown(t *testing.T) {
	// Only one trait reported; the other three are Unknown. Overall
	// should reflect just the one present field, not Unknown.
	state := newState("e1", "h1", map[string]string{
		"agent_self.open_fds": "300", // Warn
	})
	p := Compute(state)
	if p.Overall != Warn {
		t.Errorf("overall should ignore Unknown and report Warn; got %v", p.Overall)
	}
}

func TestAggregatePicksMaxOffender(t *testing.T) {
	states := []EnvoySource{
		newState("a", "alpha", map[string]string{"agent_self.open_fds": "100"}),
		newState("b", "bravo", map[string]string{"agent_self.open_fds": "750"}),
		newState("c", "charlie", map[string]string{"agent_self.open_fds": "200"}),
	}
	sum := Aggregate(states)
	if sum.MaxOpenFDs != 750 || sum.MaxOpenFDsHost != "bravo" {
		t.Errorf("expected max=750 host=bravo; got max=%d host=%q", sum.MaxOpenFDs, sum.MaxOpenFDsHost)
	}
	if sum.MaxOpenFDsID != "b" {
		t.Errorf("expected drilldown ID=b; got %q", sum.MaxOpenFDsID)
	}
	if sum.MaxOpenFDsSeverity != Fail {
		t.Errorf("expected Fail on 750; got %v", sum.MaxOpenFDsSeverity)
	}
	if sum.TotalReporting != 3 {
		t.Errorf("expected 3 reporting; got %d", sum.TotalReporting)
	}
}

func TestAggregateCountsHighFDPctAndOom(t *testing.T) {
	states := []EnvoySource{
		newState("a", "alpha", map[string]string{"host_self.fd_pct": "10.0"}),
		newState("b", "bravo", map[string]string{"host_self.fd_pct": "75.0"}),    // warn
		newState("c", "charlie", map[string]string{"host_self.fd_pct": "90.0"}), // fail
		newState("d", "delta", map[string]string{"host_self.oom_recent": "1"}),
		newState("e", "echo", map[string]string{"host_self.oom_recent": "0"}),
	}
	sum := Aggregate(states)
	if sum.HighFDPctCount != 2 {
		t.Errorf("expected 2 high-fd-pct; got %d", sum.HighFDPctCount)
	}
	if sum.HighFDPctExample != "bravo" {
		t.Errorf("expected first-seen example=bravo; got %q", sum.HighFDPctExample)
	}
	if sum.RecentOomCount != 1 {
		t.Errorf("expected 1 recent oom; got %d", sum.RecentOomCount)
	}
	if sum.RecentOomExample != "delta" {
		t.Errorf("expected oom example=delta; got %q", sum.RecentOomExample)
	}
}

func TestAggregateNoData(t *testing.T) {
	// Three envoys, none reporting any of the relevant traits — rollup
	// should distinguish "no data" from "everything green."
	states := []EnvoySource{
		newState("a", "alpha", map[string]string{}),
		newState("b", "bravo", map[string]string{"unrelated.trait": "x"}),
		newState("c", "charlie", nil),
	}
	sum := Aggregate(states)
	if sum.TotalReporting != 0 {
		t.Errorf("expected TotalReporting=0; got %d", sum.TotalReporting)
	}
	if sum.MaxOpenFDsHost != "" {
		t.Errorf("expected empty offender host; got %q", sum.MaxOpenFDsHost)
	}
}

func TestComputeNilTraitsSafe(t *testing.T) {
	// Zero-value source (nil traits) is genuinely unknown — distinct
	// from OK so the UI renders "—" for everything rather than
	// "all green."
	p := Compute(EnvoySource{})
	if p.ID != "" || p.Hostname != "" {
		t.Errorf("zero-value source should return zero identity fields; got %+v", p)
	}
	if p.Overall != Unknown {
		t.Errorf("nil-traits Overall should be Unknown; got %v", p.Overall)
	}
}
