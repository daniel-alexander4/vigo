// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
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
	sqlDB.Exec(`INSERT INTO run_results (run_id, module_name, resource_type, resource_name, action_taken) VALUES (1, 'mod', 'file', 'conf', 'updated')`)

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
