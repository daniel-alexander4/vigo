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
	if p.Health != OK {
		t.Errorf("expected Health=OK with all missing; got %v", p.Health)
	}
}

func TestBatteryHealth(t *testing.T) {
	// Absent on a host with no battery — Unknown, not present, doesn't
	// poison the composite.
	p := Compute(newState("e1", "server", map[string]string{}))
	if p.BatteryHealth.Present {
		t.Errorf("no battery trait should leave BatteryHealth Present=false")
	}
	if p.Health != OK {
		t.Errorf("absent battery must not taint composite; got Health=%v", p.Health)
	}

	// Worn battery (girlslaptop: 66.5%) classifies Fail and drives the
	// composite Health.
	worn := Compute(newState("e2", "laptop", map[string]string{"battery.health_pct": "66.5"}))
	if !worn.BatteryHealth.Present || worn.BatteryHealth.Value != 66.5 {
		t.Errorf("BatteryHealth = %+v, want Present=true Value=66.5", worn.BatteryHealth)
	}
	if worn.BatteryHealth.Severity != Fail {
		t.Errorf("66.5%% health should classify Fail; got %v", worn.BatteryHealth.Severity)
	}
	if worn.Health != Fail {
		t.Errorf("worn battery should drive composite Health to Fail; got %v", worn.Health)
	}

	// Threshold boundaries.
	cases := map[string]Severity{"69.9": Fail, "70": Warn, "79.9": Warn, "80": OK, "95": OK}
	for v, want := range cases {
		got := Compute(newState("e", "h", map[string]string{"battery.health_pct": v})).BatteryHealth.Severity
		if got != want {
			t.Errorf("health %s: classify = %v, want %v", v, got, want)
		}
	}
}

func TestHostUptime(t *testing.T) {
	// Present, informational (neutral) — never affects composite Health.
	p := Compute(newState("e1", "h1", map[string]string{"uptime.seconds": "432000"}))
	if !p.HostUptimeSecs.Present || p.HostUptimeSecs.Value != 432000 {
		t.Errorf("HostUptimeSecs = %+v, want Present=true Value=432000", p.HostUptimeSecs)
	}
	if p.HostUptimeSecs.Severity != OK {
		t.Errorf("host uptime is neutral; want Severity=OK, got %v", p.HostUptimeSecs.Severity)
	}
	if p.Health != OK {
		t.Errorf("host uptime must not affect composite; got Health=%v", p.Health)
	}
	// Absent on a host that hasn't reported it.
	if Compute(newState("e2", "h2", map[string]string{})).HostUptimeSecs.Present {
		t.Errorf("missing uptime.seconds should leave HostUptimeSecs Present=false")
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

func TestComputeHealthTakesWorst(t *testing.T) {
	state := newState("e1", "h1", map[string]string{
		"agent_self.open_fds":            "100",   // OK
		"agent_self.descendant_open_fds": "9999",  // Fail
		"host_self.fd_pct":               "10.0",  // OK
	})
	p := Compute(state)
	if p.Health != Fail {
		t.Errorf("Health should be Fail (descendants in fail band); got %v", p.Health)
	}
}

func TestComputeHealthIgnoresUnknown(t *testing.T) {
	// Only one trait reported; the others are Unknown. Health should
	// reflect just the one present field, not Unknown.
	state := newState("e1", "h1", map[string]string{
		"agent_self.open_fds": "300", // Warn
	})
	p := Compute(state)
	if p.Health != Warn {
		t.Errorf("Health should ignore Unknown and report Warn; got %v", p.Health)
	}
}

func TestComputeHealthFoldsRootfsAndInodeAndRepo(t *testing.T) {
	// Open FDs OK, but rootfs is in the fail band — Health flips Fail.
	state := newState("e1", "h1", map[string]string{
		"agent_self.open_fds":            "100",  // OK
		"mountpoints.mounts./.used_pct":  "95.0", // Fail (>90)
		"mountpoints.mounts./.inode_pct": "10.0", // OK
		"package_updates.count":          "12",   // collector ran, no broken repos → OK
	})
	p := Compute(state)
	if p.RootfsPct.Severity != Fail {
		t.Errorf("rootfs 95%% should be Fail; got %v", p.RootfsPct.Severity)
	}
	if p.InodePct.Severity != OK {
		t.Errorf("inode 10%% should be OK; got %v", p.InodePct.Severity)
	}
	if p.RepoHealth.Severity != OK {
		t.Errorf("package_updates.count=12 (no broken) should be OK; got %v", p.RepoHealth.Severity)
	}
	if p.Health != Fail {
		t.Errorf("Health should fold rootfs into composite (Fail); got %v", p.Health)
	}
}

func TestRepoHealthFromPackageUpdates(t *testing.T) {
	cases := []struct {
		name       string
		traits     map[string]string
		wantSev    Severity
		wantErrors uint64
		wantPres   bool
	}{
		{"no collector run", map[string]string{}, Unknown, 0, false},
		{"unsupported pkg mgr (count=-1)", map[string]string{"package_updates.count": "-1"}, Unknown, 0, false},
		{"healthy", map[string]string{"package_updates.count": "5"}, OK, 0, true},
		{"healthy (count=0)", map[string]string{"package_updates.count": "0"}, OK, 0, true},
		{"broken", map[string]string{
			"package_updates.count":             "5",
			"package_updates.broken_repo_count": "3",
		}, Fail, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Compute(EnvoySource{ID: "e1", Hostname: "h1", Traits: tc.traits})
			if p.RepoHealth.Severity != tc.wantSev {
				t.Errorf("severity: want %v, got %v", tc.wantSev, p.RepoHealth.Severity)
			}
			if p.RepoHealth.Errors != tc.wantErrors {
				t.Errorf("errors: want %d, got %d", tc.wantErrors, p.RepoHealth.Errors)
			}
			if p.RepoHealth.Present != tc.wantPres {
				t.Errorf("present: want %v, got %v", tc.wantPres, p.RepoHealth.Present)
			}
		})
	}
}

func TestAggregatePicksMaxOffender(t *testing.T) {
	states := []EnvoySource{
		newState("a", "alpha", map[string]string{"agent_self.open_fds": "100"}),
		newState("b", "bravo", map[string]string{"agent_self.open_fds": "750"}),
		newState("c", "charlie", map[string]string{"agent_self.open_fds": "200"}),
	}
	sum, perEnvoy := Aggregate(states)
	if len(perEnvoy) != 3 {
		t.Errorf("expected 3 per-envoy projections; got %d", len(perEnvoy))
	}
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
	sum, _ := Aggregate(states)
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
	sum, _ := Aggregate(states)
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
	if p.Health != Unknown {
		t.Errorf("nil-traits Health should be Unknown; got %v", p.Health)
	}
}

func TestUptimeMetric(t *testing.T) {
	if m := UptimeMetric(map[string]string{"agent_self.uptime_secs": "172800"}); !m.Present || m.Value != 172800 {
		t.Errorf("present uptime: got %+v, want Present=true Value=172800", m)
	}
	if m := UptimeMetric(map[string]string{"agent_self.open_fds": "100"}); m.Present {
		t.Errorf("missing uptime trait should yield Present=false; got %+v", m)
	}
	if m := UptimeMetric(nil); m.Present {
		t.Errorf("nil traits should yield Present=false; got %+v", m)
	}
}

func TestAggregateRollupWorstSeverityAndDegradedCount(t *testing.T) {
	states := []EnvoySource{
		newState("a", "alpha", map[string]string{"agent_self.open_fds": "100"}),                 // OK
		newState("b", "bravo", map[string]string{"agent_self.open_fds": "300"}),                 // Warn
		newState("c", "charlie", map[string]string{"mountpoints.mounts./.used_pct": "95.0"}),    // Fail
		newState("d", "delta", map[string]string{}),                                              // Unknown
	}
	sum, perEnvoy := Aggregate(states)
	if len(perEnvoy) != 4 {
		t.Fatalf("expected 4 per-envoy projections; got %d", len(perEnvoy))
	}
	if sum.WorstSeverity != Fail {
		t.Errorf("WorstSeverity should be Fail (charlie at 95%% rootfs); got %v", sum.WorstSeverity)
	}
	if sum.DegradedCount != 2 {
		t.Errorf("DegradedCount should count bravo + charlie (= 2); got %d", sum.DegradedCount)
	}
}
