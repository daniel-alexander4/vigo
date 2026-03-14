// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hobnob/hobnob/server/db"
)

// openTestDB creates an in-memory SQLite database with the minimal thralls
// table required by the Collector.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", "file::memory:?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE thralls (
			id                       TEXT PRIMARY KEY,
			hostname                 TEXT NOT NULL,
			ip_address               TEXT NOT NULL,
			pubkey_pem               TEXT NOT NULL,
			enrolled_at              DATETIME NOT NULL DEFAULT (datetime('now')),
			last_seen                DATETIME,
			revoked                  INTEGER NOT NULL DEFAULT 0,
			revoked_at               DATETIME,
			force_push               INTEGER NOT NULL DEFAULT 0,
			pending_rotated_secrets  TEXT NOT NULL DEFAULT '[]',
			tags                     TEXT NOT NULL DEFAULT '',
			maintenance_until        DATETIME
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB
}

// insertThrall inserts a thrall row with the given attributes.
func insertThrall(t *testing.T, sqlDB *sql.DB, id, hostname string, lastSeen *time.Time, revoked bool) {
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
		`INSERT INTO thralls (id, hostname, ip_address, pubkey_pem, last_seen, revoked)
		 VALUES (?, ?, '10.0.0.1', 'pubkey', ?, ?)`,
		id, hostname, ls, rev,
	)
	if err != nil {
		t.Fatalf("insert thrall %s: %v", id, err)
	}
}

// TestCollector_ConcurrentUpdateGauges runs 20 goroutines calling
// UpdateGauges concurrently to verify no data races on the Prometheus
// metric writes or the underlying DB reads.
func TestCollector_ConcurrentUpdateGauges(t *testing.T) {
	sqlDB := openTestDB(t)
	now := time.Now()

	// Insert a handful of thralls so there's real data to read.
	for i := 0; i < 10; i++ {
		revoked := i >= 8
		var ls *time.Time
		if i < 7 {
			seen := now.Add(-time.Duration(i) * time.Minute)
			ls = &seen
		}
		insertThrall(t, sqlDB, fmt.Sprintf("t-%d", i), fmt.Sprintf("h-%d.example.com", i), ls, revoked)
	}

	c := NewCollector(db.New(sqlDB), sqlDB)
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

// TestCollector_UpdateGaugesConsistency inserts a known set of thralls,
// calls UpdateGauges once, and verifies the Prometheus gauge values
// match expectations.
func TestCollector_UpdateGaugesConsistency(t *testing.T) {
	sqlDB := openTestDB(t)
	now := time.Now()

	// 5 total thralls:
	// - t-0: active (seen 1 min ago), not revoked
	// - t-1: active (seen 30 min ago), not revoked
	// - t-2: stale (seen 2 hours ago), not revoked
	// - t-3: never seen, not revoked
	// - t-4: active (seen 5 min ago), revoked
	seen0 := now.Add(-1 * time.Minute)
	seen1 := now.Add(-30 * time.Minute)
	seen2 := now.Add(-2 * time.Hour)
	seen4 := now.Add(-5 * time.Minute)

	insertThrall(t, sqlDB, "t-0", "active0.example.com", &seen0, false)
	insertThrall(t, sqlDB, "t-1", "active1.example.com", &seen1, false)
	insertThrall(t, sqlDB, "t-2", "stale.example.com", &seen2, false)
	insertThrall(t, sqlDB, "t-3", "noseen.example.com", nil, false)
	insertThrall(t, sqlDB, "t-4", "revoked.example.com", &seen4, true)

	c := NewCollector(db.New(sqlDB), sqlDB)
	ctx := context.Background()

	if err := c.UpdateGauges(ctx); err != nil {
		t.Fatalf("UpdateGauges: %v", err)
	}

	// ThrallsTotal = 5 (all thralls, including revoked).
	gotTotal := testutil.ToFloat64(ThrallsTotal)
	if gotTotal != 5 {
		t.Errorf("ThrallsTotal = %v, want 5", gotTotal)
	}

	// ThrallsActive = 2 (t-0 and t-1 seen within the last hour; t-4 is seen
	// recently but revoked still counts as "active" in the metric since it only
	// checks LastSeen).
	// Actually the code checks recentlyActive for ALL thralls, revoked or not.
	// t-0 (1 min), t-1 (30 min), t-4 (5 min) = 3 active.
	gotActive := testutil.ToFloat64(ThrallsActive)
	if gotActive != 3 {
		t.Errorf("ThrallsActive = %v, want 3", gotActive)
	}

	// CompliancePct = compliant / totalNonRevoked.
	// Non-revoked: t-0, t-1, t-2, t-3 (4 total).
	// recentlyActive among non-revoked: t-0, t-1 (2 compliant).
	// CompliancePct = 2/4 = 0.5.
	gotPct := testutil.ToFloat64(CompliancePct)
	if math.Abs(gotPct-0.5) > 0.001 {
		t.Errorf("CompliancePct = %v, want 0.5", gotPct)
	}
}
