// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func openRetentionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", "file::memory:?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	schema := `
		CREATE TABLE envoys (
			id TEXT PRIMARY KEY, hostname TEXT NOT NULL, ip_address TEXT NOT NULL,
			pubkey_pem TEXT NOT NULL, enrolled_at DATETIME NOT NULL DEFAULT (datetime('now')),
			last_seen DATETIME, revoked INTEGER NOT NULL DEFAULT 0, revoked_at DATETIME,
			force_push INTEGER NOT NULL DEFAULT 0, pending_rotated_secrets TEXT NOT NULL DEFAULT '[]',
			tags TEXT NOT NULL DEFAULT '', maintenance_until DATETIME
		);
		CREATE TABLE runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, envoy_id TEXT NOT NULL REFERENCES envoys(id),
			started_at DATETIME NOT NULL DEFAULT (datetime('now')), finished_at DATETIME,
			status TEXT NOT NULL DEFAULT 'running', module_count INTEGER NOT NULL DEFAULT 0,
			changed_count INTEGER NOT NULL DEFAULT 0, failed_count INTEGER NOT NULL DEFAULT 0,
			skipped_count INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE run_results (
			id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			module_name TEXT, resource_type TEXT, resource_name TEXT, action_taken TEXT,
			changed INTEGER NOT NULL DEFAULT 0, error_message TEXT, duration_ms INTEGER, created_at DATETIME DEFAULT (datetime('now'))
		);
		CREATE TABLE push_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, command TEXT NOT NULL, target_glob TEXT NOT NULL,
			operator TEXT, status TEXT NOT NULL DEFAULT 'pending', total_targets INTEGER NOT NULL DEFAULT 0,
			timeout_secs INTEGER, batch_size INTEGER, max_failures INTEGER,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')), started_at DATETIME, finished_at DATETIME
		);
		CREATE TABLE push_run_targets (
			id INTEGER PRIMARY KEY AUTOINCREMENT, push_run_id INTEGER NOT NULL REFERENCES push_runs(id) ON DELETE CASCADE,
			envoy_id TEXT, hostname TEXT, status TEXT NOT NULL DEFAULT 'pending',
			exit_code INTEGER, stdout TEXT, stderr TEXT, duration_ms INTEGER, error_msg TEXT,
			dispatched_at DATETIME, finished_at DATETIME
		);
		CREATE TABLE workflow_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, workflow_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending', current_step INTEGER NOT NULL DEFAULT 0,
			total_steps INTEGER NOT NULL DEFAULT 0, operator TEXT, failure_reason TEXT,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')), started_at DATETIME, finished_at DATETIME
		);
		CREATE TABLE workflow_step_results (
			id INTEGER PRIMARY KEY AUTOINCREMENT, workflow_run_id INTEGER NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
			step_index INTEGER NOT NULL, step_name TEXT, push_run_id INTEGER,
			status TEXT NOT NULL DEFAULT 'pending', started_at DATETIME, finished_at DATETIME
		);
		CREATE TABLE convergence_history (
			id TEXT PRIMARY KEY, recorded_at TEXT NOT NULL,
			total INTEGER, converged INTEGER, recurring_drift INTEGER,
			errors INTEGER, lost_contact INTEGER, no_data INTEGER,
			UNIQUE(recorded_at)
		);
		CREATE TABLE compliance_history (
			id TEXT PRIMARY KEY, snapshot_date TEXT NOT NULL,
			framework TEXT NOT NULL, score_percent REAL NOT NULL DEFAULT 0,
			satisfied INTEGER NOT NULL DEFAULT 0, partial INTEGER NOT NULL DEFAULT 0,
			not_satisfied INTEGER NOT NULL DEFAULT 0, total_controls INTEGER NOT NULL DEFAULT 0,
			UNIQUE(snapshot_date, framework)
		);
		CREATE TABLE file_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT, envoy_id TEXT, path TEXT, content_hash TEXT,
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
	`
	if _, err := sqlDB.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return sqlDB
}

func TestRetentionPruner_PrunesOldRows(t *testing.T) {
	sqlDB := openRetentionTestDB(t)
	queries := New(sqlDB)
	ctx := context.Background()

	// Insert a envoy for FK constraints.
	sqlDB.Exec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('t1', 'h1', '10.0.0.1', 'key')`)

	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	recent := time.Now().Add(-1 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	// Insert old and recent runs.
	sqlDB.Exec(`INSERT INTO runs (envoy_id, started_at, status) VALUES ('t1', ?, 'complete')`, old)
	sqlDB.Exec(`INSERT INTO runs (envoy_id, started_at, status) VALUES ('t1', ?, 'complete')`, recent)
	// Insert a running run that's old — should NOT be pruned.
	sqlDB.Exec(`INSERT INTO runs (envoy_id, started_at, status) VALUES ('t1', ?, 'running')`, old)

	// Old and recent task runs.
	sqlDB.Exec(`INSERT INTO push_runs (command, target_glob, status, total_targets, created_at) VALUES ('echo', '*', 'complete', 1, ?)`, old)
	sqlDB.Exec(`INSERT INTO push_runs (command, target_glob, status, total_targets, created_at) VALUES ('echo', '*', 'complete', 1, ?)`, recent)

	// Old and recent workflow runs.
	sqlDB.Exec(`INSERT INTO workflow_runs (workflow_name, status, total_steps, created_at) VALUES ('w1', 'complete', 1, ?)`, old)
	sqlDB.Exec(`INSERT INTO workflow_runs (workflow_name, status, total_steps, created_at) VALUES ('w1', 'complete', 1, ?)`, recent)

	// Old and recent convergence history.
	sqlDB.Exec(`INSERT INTO convergence_history (id, recorded_at, total, converged, recurring_drift, errors, lost_contact, no_data) VALUES ('ch1', '2024-01-01', 10, 8, 1, 0, 1, 0)`)
	sqlDB.Exec(`INSERT INTO convergence_history (id, recorded_at, total, converged, recurring_drift, errors, lost_contact, no_data) VALUES ('ch2', '2026-03-01', 10, 9, 0, 0, 1, 0)`)

	// Old and recent compliance history.
	sqlDB.Exec(`INSERT INTO compliance_history (id, snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls) VALUES ('co1', '2024-01-01', 'cis', 80.0, 8, 1, 1, 10)`)
	sqlDB.Exec(`INSERT INTO compliance_history (id, snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls) VALUES ('co2', '2026-03-01', 'cis', 90.0, 9, 1, 0, 10)`)

	// Prune with 90-day retention.
	pruner := NewRetentionPruner(queries, sqlDB, 90*24*time.Hour)
	pruner.Prune(ctx)

	// Verify: old complete rows pruned, recent rows kept, running rows kept.
	var runCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM runs").Scan(&runCount)
	if runCount != 2 { // recent + running
		t.Errorf("runs count = %d, want 2 (recent + running)", runCount)
	}

	var pushRunCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM push_runs").Scan(&pushRunCount)
	if pushRunCount != 1 {
		t.Errorf("push_runs count = %d, want 1", pushRunCount)
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
	sqlDB.Exec(`INSERT INTO runs (id, envoy_id, started_at, status) VALUES (1, 't1', ?, 'complete')`, old)
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
