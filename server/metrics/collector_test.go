// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"context"
	"math"

	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeFleet is a FleetSource with fixed content.
type fakeFleet struct {
	summary ConvergenceSnapshot
	envoys  []EnvoySnapshot
}

func (f *fakeFleet) ConvergenceSummary() ConvergenceSnapshot { return f.summary }
func (f *fakeFleet) AllEnvoySnapshots() []EnvoySnapshot      { return f.envoys }

func testFleet(now time.Time) *fakeFleet {
	// With a 1m configured interval the default stale boundary is 2.5m.
	return &fakeFleet{
		summary: ConvergenceSnapshot{Total: 4, Converged: 2, Degraded: 1, NoData: 1, Offline: 1},
		envoys: []EnvoySnapshot{
			{ID: "t-0", LastSeen: now.Add(-10 * time.Second)},  // fresh
			{ID: "t-1", LastSeen: now.Add(-100 * time.Second)}, // fresh (inside 2.5m)
			// Slow agent: 10m observed cycle → 25m boundary, so 20m is fresh
			// under the freshness SoT even though a fixed window would differ.
			{ID: "t-2", LastSeen: now.Add(-20 * time.Minute), ObservedCycle: 10 * time.Minute},
			// Stale past 2.5m, yet inside any fixed one-hour window.
			{ID: "t-3", LastSeen: now.Add(-9 * time.Minute)},
			{ID: "t-4"}, // never seen
			{ID: "t-5", Revoked: true, LastSeen: now.Add(-5 * time.Second)}, // revoked
		},
	}
}

// TestCollector_ConcurrentUpdateGauges runs 20 goroutines calling
// UpdateGauges concurrently to verify no data races on the metric writes.
func TestCollector_ConcurrentUpdateGauges(t *testing.T) {
	m := NewTestMetrics()
	c := NewCollector(nil, testFleet(time.Now()), nil, nil, m, time.Minute, 0)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.UpdateGauges(ctx); err != nil {
				t.Errorf("UpdateGauges: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestCollector_UpdateGaugesFromFleetIndex pins every fleet gauge to the
// FleetIndex snapshot and the SoT packages: total from the non-revoked
// counter, active from the freshness predicate, convergence % from
// convergence.ConvergedPct over (Total - Offline).
func TestCollector_UpdateGaugesFromFleetIndex(t *testing.T) {
	m := NewTestMetrics()
	c := NewCollector(nil, testFleet(time.Now()), nil, nil, m, time.Minute, 0)
	if err := c.UpdateGauges(context.Background()); err != nil {
		t.Fatalf("UpdateGauges: %v", err)
	}

	if got := testutil.ToFloat64(m.EnvoysTotal); got != 4 {
		t.Errorf("EnvoysTotal = %v, want 4 (non-revoked fleet)", got)
	}
	// t-0, t-1, t-2 fresh; t-3 stale; t-4 never seen; t-5 revoked.
	if got := testutil.ToFloat64(m.EnvoysActive); got != 3 {
		t.Errorf("EnvoysActive = %v, want 3", got)
	}
	// 2 converged / (4 total - 1 offline) = 0.667.
	if got := testutil.ToFloat64(m.ConvergencePct); math.Abs(got-2.0/3.0) > 0.001 {
		t.Errorf("ConvergencePct = %v, want 0.667", got)
	}
}

// TestCollector_CheckinAgeIsSnapshot proves the age distribution reflects the
// fleet now and is not accumulated across ticks: two ticks over the same four
// seen, non-revoked envoys must still expose a count of 4.
func TestCollector_CheckinAgeIsSnapshot(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	c := NewCollector(nil, testFleet(time.Now()), nil, nil, m, time.Minute, 0)
	for i := 0; i < 2; i++ {
		if err := c.UpdateGauges(context.Background()); err != nil {
			t.Fatalf("UpdateGauges: %v", err)
		}
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var found bool
	for _, mf := range mfs {
		if mf.GetName() != "vigo_checkin_age_seconds" {
			continue
		}
		found = true
		h := mf.GetMetric()[0].GetHistogram()
		// Ages 10s, 100s, 1200s, 540s (never-seen and revoked excluded).
		if h.GetSampleCount() != 4 {
			t.Errorf("sample count = %d, want 4 (snapshot, not cumulative)", h.GetSampleCount())
		}
		want := map[float64]uint64{5: 0, 15: 1, 120: 2, 900: 3, 1800: 4, 7200: 4}
		for _, b := range h.GetBucket() {
			if w, ok := want[b.GetUpperBound()]; ok && b.GetCumulativeCount() != w {
				t.Errorf("bucket le=%v = %d, want %d", b.GetUpperBound(), b.GetCumulativeCount(), w)
			}
		}
	}
	if !found {
		t.Fatal("vigo_checkin_age_seconds not gathered")
	}
}
func TestUpdateSwarmMetrics(t *testing.T) {
	m := NewTestMetrics()
	snap := SwarmSnapshot{
		SubstrateEnvoys:          1,
		ManifestActive:           2,
		FootprintBytes:           3,
		GitbackProjects:          4,
		GitbackEnvoys:            5,
		GitbackDivergentRefs:     6,
		CuratorArtifacts:         7,
		CuratorPublishers:        8,
		LockboxUsers:             9,
		LockboxFiles:             10,
		LockboxDivergentFiles:    11,
		LongdrawerUsers:          12,
		LongdrawerFiles:          13,
		LongdrawerDivergentFiles: 14,
		SpannerBoltsActive:       15,
		SpannerBoltsRevoked:      16,
	}
	updateSwarmMetrics(m, snap)

	// Distinct values per gauge so a mis-wired field is caught.
	checks := []struct {
		name string
		g    prometheus.Gauge
		want float64
	}{
		{"swarm_envoys_substrate_active", m.SwarmSubstrateEnvoys, 1},
		{"swarm_manifest_entries_active", m.SwarmManifestActive, 2},
		{"swarm_footprint_bytes", m.SwarmFootprintBytes, 3},
		{"gitback_projects", m.GitbackProjects, 4},
		{"gitback_envoys", m.GitbackEnvoys, 5},
		{"gitback_divergent_refs", m.GitbackDivergentRefs, 6},
		{"curator_artifacts", m.CuratorArtifacts, 7},
		{"curator_publishers", m.CuratorPublishers, 8},
		{"lockbox_users", m.LockboxUsers, 9},
		{"lockbox_files", m.LockboxFiles, 10},
		{"lockbox_divergent_files", m.LockboxDivergentFiles, 11},
		{"longdrawer_users", m.LongdrawerUsers, 12},
		{"longdrawer_files", m.LongdrawerFiles, 13},
		{"longdrawer_divergent_files", m.LongdrawerDivergentFiles, 14},
		{"spanner_bolts_active", m.SpannerBoltsActive, 15},
		{"spanner_bolts_revoked", m.SpannerBoltsRevoked, 16},
	}
	for _, c := range checks {
		if got := testutil.ToFloat64(c.g); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}
