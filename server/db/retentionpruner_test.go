// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/testutil"
)

func openRetentionTestDB(t *testing.T) *sql.DB {
	return testutil.OpenMigratedTestDB(t)
}

func TestRetentionPruner_PrunesOldRows(t *testing.T) {
	sqlDB := openRetentionTestDB(t)
	queries := New(sqlDB)
	ctx := context.Background()

	// Insert a envoy for FK constraints.
	sqlDB.Exec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('t1', 'h1', '10.0.0.1', 'key')`)

	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	recent := time.Now().Add(-1 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	// History tables (convergence_history, compliance_history) are pruned
	// date-only against cutoff.Format("2006-01-02"), so their fixtures use
	// date-only relative values — never literal dates, which age out of the
	// retention window as wall-clock advances.
	oldDate := time.Now().Add(-100 * 24 * time.Hour).UTC().Format("2006-01-02")
	recentDate := time.Now().UTC().Format("2006-01-02")

	// Insert old and recent runs (migration CHECK allows: running, success, failure, partial).
	sqlDB.Exec(`INSERT INTO runs (envoy_id, started_at, status) VALUES ('t1', ?, 'success')`, old)
	sqlDB.Exec(`INSERT INTO runs (envoy_id, started_at, status) VALUES ('t1', ?, 'success')`, recent)
	// Insert a running run that's old — should NOT be pruned.
	sqlDB.Exec(`INSERT INTO runs (envoy_id, started_at, status) VALUES ('t1', ?, 'running')`, old)

	// Old and recent task runs (migration CHECK allows: pending, running, complete, failed, cancelled).
	sqlDB.Exec(`INSERT INTO task_runs (command, target_glob, status, total_targets, created_at) VALUES ('longdrawer', '*', 'complete', 1, ?)`, old)
	sqlDB.Exec(`INSERT INTO task_runs (command, target_glob, status, total_targets, created_at) VALUES ('longdrawer', '*', 'complete', 1, ?)`, recent)

	// Old and recent workflow runs (migration CHECK allows: pending, running, complete, failed, aborted).
	sqlDB.Exec(`INSERT INTO workflow_runs (workflow_name, status, total_steps, created_at) VALUES ('w1', 'complete', 1, ?)`, old)
	sqlDB.Exec(`INSERT INTO workflow_runs (workflow_name, status, total_steps, created_at) VALUES ('w1', 'complete', 1, ?)`, recent)

	// Old and recent convergence history (post axis-split: failure axis
	// columns + drift axis columns + offline).
	sqlDB.Exec(`INSERT INTO convergence_history (id, recorded_at, total, converged, degraded, failed, no_data, changed, diverged, offline) VALUES ('ch1', ?, 10, 8, 1, 0, 0, 2, 0, 1)`, oldDate)
	sqlDB.Exec(`INSERT INTO convergence_history (id, recorded_at, total, converged, degraded, failed, no_data, changed, diverged, offline) VALUES ('ch2', ?, 10, 9, 0, 0, 0, 1, 0, 1)`, recentDate)

	// Old and recent compliance history.
	sqlDB.Exec(`INSERT INTO compliance_history (id, snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls) VALUES ('co1', ?, 'cis', 80.0, 8, 1, 1, 10)`, oldDate)
	sqlDB.Exec(`INSERT INTO compliance_history (id, snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls) VALUES ('co2', ?, 'cis', 90.0, 9, 1, 0, 10)`, recentDate)

	// Prune with 90-day retention.
	pruner := NewRetentionPruner(queries, sqlDB, 90*24*time.Hour)
	pruner.Prune(ctx)

	// Verify: old complete rows pruned, recent rows kept, running rows kept.
	var runCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM runs").Scan(&runCount)
	if runCount != 2 { // recent + running
		t.Errorf("runs count = %d, want 2 (recent + running)", runCount)
	}

	var taskRunCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM task_runs").Scan(&taskRunCount)
	if taskRunCount != 1 {
		t.Errorf("task_runs count = %d, want 1", taskRunCount)
	}

	var workflowRunCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM workflow_runs").Scan(&workflowRunCount)
	if workflowRunCount != 1 {
		t.Errorf("workflow_runs count = %d, want 1", workflowRunCount)
	}

	var convergenceCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM convergence_history").Scan(&convergenceCount)
	if convergenceCount != 1 {
		t.Errorf("convergence_history count = %d, want 1", convergenceCount)
	}

	var complianceCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM compliance_history").Scan(&complianceCount)
	if complianceCount != 1 {
		t.Errorf("compliance_history count = %d, want 1", complianceCount)
	}
}

func TestRetentionPruner_CascadeDeletesRunResults(t *testing.T) {
	sqlDB := openRetentionTestDB(t)
	queries := New(sqlDB)
	ctx := context.Background()

	sqlDB.Exec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('t1', 'h1', '10.0.0.1', 'key')`)

	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	sqlDB.Exec(`INSERT INTO runs (id, envoy_id, started_at, status) VALUES (1, 't1', ?, 'success')`, old)
	sqlDB.Exec(`INSERT INTO run_results (run_id, configcrate_name, resource_type, resource_name, action_taken) VALUES (1, 'mod', 'file', 'conf', 'updated')`)

	var resultsBefore int
	sqlDB.QueryRow("SELECT COUNT(*) FROM run_results").Scan(&resultsBefore)
	if resultsBefore != 1 {
		t.Fatalf("expected 1 run_result before prune, got %d", resultsBefore)
	}

	pruner := NewRetentionPruner(queries, sqlDB, 90*24*time.Hour)
	pruner.Prune(ctx)

	var resultsAfter int
	sqlDB.QueryRow("SELECT COUNT(*) FROM run_results").Scan(&resultsAfter)
	if resultsAfter != 0 {
		t.Errorf("run_results count = %d after prune, want 0 (cascade delete)", resultsAfter)
	}
}

// TestRetentionPruner_DrainsBacklogLargerThanBatch proves the batched loop
// runs more than one iteration: a backlog larger than retentionBatchSize is
// fully drained, and the run_results cascade follows across batches.
func TestRetentionPruner_DrainsBacklogLargerThanBatch(t *testing.T) {
	sqlDB := openRetentionTestDB(t)
	queries := New(sqlDB)
	ctx := context.Background()

	sqlDB.Exec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('t1', 'h1', '10.0.0.1', 'key')`)
	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	const n = retentionBatchSize + 5 // forces a second (short) batch
	tx, err := sqlDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		rid := fmt.Sprintf("r%d", i)
		if _, err := tx.Exec(`INSERT INTO runs (id, envoy_id, started_at, status) VALUES (?, 't1', ?, 'success')`, rid, old); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO run_results (run_id, configcrate_name, resource_type, resource_name, action_taken) VALUES (?, 'mod', 'file', 'conf', 'updated')`, rid); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	pruner := NewRetentionPruner(queries, sqlDB, 90*24*time.Hour)
	res := pruner.Prune(ctx)

	if res.Deleted["runs"] != int64(n) {
		t.Errorf("pruned runs = %d, want %d", res.Deleted["runs"], n)
	}
	var runCount, resultCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM runs").Scan(&runCount)
	sqlDB.QueryRow("SELECT COUNT(*) FROM run_results").Scan(&resultCount)
	if runCount != 0 {
		t.Errorf("runs remaining = %d, want 0 (backlog fully drained)", runCount)
	}
	if resultCount != 0 {
		t.Errorf("run_results remaining = %d, want 0 (cascade across batches)", resultCount)
	}
}

// TestDeleteEnvoy_ClearsApprovedPatternUsedBy is the regression test for the
// 000020 FK fix: deleting an envoy that consumed an approved pattern must
// succeed (it previously failed with "FOREIGN KEY constraint failed"), and the
// consumed-pattern record survives with used_by cleared (SET NULL).
func TestDeleteEnvoy_ClearsApprovedPatternUsedBy(t *testing.T) {
	sqlDB := openRetentionTestDB(t)
	queries := New(sqlDB)
	ctx := context.Background()

	sqlDB.Exec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('e1', 'h1', '10.0.0.1', 'key')`)
	if _, err := sqlDB.Exec(`INSERT INTO approved_patterns (id, pattern, token_hash, used_at, used_by) VALUES ('p1', '*.web', 'hash', datetime('now'), 'e1')`); err != nil {
		t.Fatalf("insert approved_pattern: %v", err)
	}

	if err := queries.DeleteEnvoy(ctx, "e1"); err != nil {
		t.Fatalf("DeleteEnvoy with a consumed approved_pattern: %v", err)
	}

	var count int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM approved_patterns WHERE id = 'p1'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("approved_patterns row count = %d, want 1 (consumed-pattern record preserved)", count)
	}
	var usedBy sql.NullString
	sqlDB.QueryRow("SELECT used_by FROM approved_patterns WHERE id = 'p1'").Scan(&usedBy)
	if usedBy.Valid {
		t.Errorf("used_by = %q, want NULL after envoy delete (SET NULL)", usedBy.String)
	}
}

func TestVacuumRecommended(t *testing.T) {
	const pageSize = 4096
	cases := []struct {
		name      string
		freelist  int64
		pageCount int64
		want      bool
	}{
		// >25% of pages AND >100 MiB reclaimable → recommend.
		{"bloated", 40000, 100000, true},
		// Large fraction but tiny DB (<100 MiB freelist) → healthy slack, no.
		{"small db high pct", 2000, 4000, false},
		// Large absolute freelist but <25% of a big DB → reuse slack, no.
		{"large db low pct", 30000, 620000, false}, // ~the live-DB shape (~4.5%)
		{"empty freelist", 0, 100000, false},
		{"empty db", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vacuumRecommended(c.freelist, c.pageCount, pageSize); got != c.want {
				t.Errorf("vacuumRecommended(%d, %d, %d) = %v, want %v", c.freelist, c.pageCount, pageSize, got, c.want)
			}
		})
	}
}

func TestDatabaseSizeBytes(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	// Create a table and insert data so the DB has pages.
	sqlDB.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, data TEXT)")
	sqlDB.Exec("INSERT INTO test (data) VALUES ('hello world')")

	size, err := DatabaseSizeBytes(sqlDB)
	if err != nil {
		t.Fatalf("DatabaseSizeBytes: %v", err)
	}
	if size <= 0 {
		t.Errorf("expected positive size, got %d", size)
	}
}
