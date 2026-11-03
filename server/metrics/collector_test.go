// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"git.alexander4.io/vigo/server/db"
	vigotestutil "git.alexander4.io/vigo/server/testutil"
)

// openTestDB returns an in-memory SQLite database with the full migration
// schema. Delegates to the shared testutil package.
func openTestDB(t *testing.T) *sql.DB {
	return vigotestutil.OpenMigratedTestDB(t)
}

// insertEnvoy inserts a envoy row with the given attributes.
func insertEnvoy(t *testing.T, sqlDB *sql.DB, id, hostname string, lastSeen *time.Time, revoked bool) {
	t.Helper()
	var ls interface{}
	if lastSeen != nil {
		ls = lastSeen.UTC().Format("2006-01-02 15:04:05")
	}
	rev := 0
	if revoked {
		rev = 1
	}
	_, err := sqlDB.Exec(
		`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem, last_seen, revoked)
		 VALUES (?, ?, '10.0.0.1', 'pubkey', ?, ?)`,
		id, hostname, ls, rev,
	)
	if err != nil {
		t.Fatalf("insert envoy %s: %v", id, err)
	}
}

// TestCollector_ConcurrentUpdateGauges runs 20 goroutines calling
// UpdateGauges concurrently to verify no data races on the Prometheus
// metric writes or the underlying DB reads.
func TestCollector_ConcurrentUpdateGauges(t *testing.T) {
	sqlDB := openTestDB(t)
	now := time.Now()

	// Insert a handful of envoys so there's real data to read.
	for i := 0; i < 10; i++ {
		revoked := i >= 8
		var ls *time.Time
		if i < 7 {
			seen := now.Add(-time.Duration(i) * time.Minute)
			ls = &seen
		}
		insertEnvoy(t, sqlDB, fmt.Sprintf("t-%d", i), fmt.Sprintf("h-%d.example.com", i), ls, revoked)
	}

	m := NewTestMetrics()
	c := NewCollector(db.New(sqlDB), sqlDB, nil, nil, m, time.Minute, 0)
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
	// No panics or races = pass.
}

// TestCollector_UpdateGaugesConsistency inserts a known set of envoys,
// calls UpdateGauges once, and verifies the Prometheus gauge values
// match expectations.
func TestCollector_UpdateGaugesConsistency(t *testing.T) {
	sqlDB := openTestDB(t)
	now := time.Now()

	// 5 total envoys:
	// - t-0: active (seen 1 min ago), not revoked
	// - t-1: active (seen 30 min ago), not revoked
	// - t-2: stale (seen 2 hours ago), not revoked
	// - t-3: never seen, not revoked
	// - t-4: active (seen 5 min ago), revoked
	seen0 := now.Add(-1 * time.Minute)
	seen1 := now.Add(-30 * time.Minute)
	seen2 := now.Add(-2 * time.Hour)
	seen4 := now.Add(-5 * time.Minute)

	insertEnvoy(t, sqlDB, "t-0", "active0.example.com", &seen0, false)
	insertEnvoy(t, sqlDB, "t-1", "active1.example.com", &seen1, false)
	insertEnvoy(t, sqlDB, "t-2", "stale.example.com", &seen2, false)
	insertEnvoy(t, sqlDB, "t-3", "noseen.example.com", nil, false)
	insertEnvoy(t, sqlDB, "t-4", "revoked.example.com", &seen4, true)

	m := NewTestMetrics()
	c := NewCollector(db.New(sqlDB), sqlDB, nil, nil, m, time.Minute, 0)
	ctx := context.Background()

	if err := c.UpdateGauges(ctx); err != nil {
		t.Fatalf("UpdateGauges: %v", err)
	}

	// EnvoysTotal = 5 (all envoys, including revoked).
	gotTotal := testutil.ToFloat64(m.EnvoysTotal)
	if gotTotal != 5 {
		t.Errorf("EnvoysTotal = %v, want 5", gotTotal)
	}

	// EnvoysActive = 2 (t-0 and t-1 seen within the last hour; t-4 is seen
	// recently but revoked still counts as "active" in the metric since it only
	// checks LastSeen).
	// Actually the code checks recentlyActive for ALL envoys, revoked or not.
	// t-0 (1 min), t-1 (30 min), t-4 (5 min) = 3 active.
	gotActive := testutil.ToFloat64(m.EnvoysActive)
	if gotActive != 3 {
		t.Errorf("EnvoysActive = %v, want 3", gotActive)
	}

	// ConvergencePct is now derived from the convergence SoT
	// (ConvergenceSummary), not a DB "recently active" proxy. This collector
	// has a nil fleetSource, so the gauge is left at its default 0.
	gotPct := testutil.ToFloat64(m.ConvergencePct)
	if math.Abs(gotPct-0) > 0.001 {
		t.Errorf("ConvergencePct = %v, want 0 (no fleetSource)", gotPct)
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
