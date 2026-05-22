// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package task

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
)

// openReaperTestDB creates an in-memory SQLite database with push tables.
// CHECK constraints on status are omitted so the reaper's "timeout" status
// update works in the test environment (the production schema does not list
// "timeout" in task_runs CHECK but the query sets it).
func openReaperTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=off")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE task_runs (
			id             TEXT PRIMARY KEY,
			command        TEXT NOT NULL,
			target_glob    TEXT NOT NULL,
			operator       TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT 'pending',
			total_targets  INTEGER NOT NULL DEFAULT 0,
			created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
			started_at     DATETIME,
			finished_at    DATETIME,
			timeout_secs   INTEGER NOT NULL DEFAULT 60,
			batch_size     TEXT,
			max_failures   TEXT
		);
		CREATE TABLE task_run_targets (
			id             TEXT PRIMARY KEY,
			task_run_id    TEXT NOT NULL,
			envoy_id      TEXT NOT NULL,
			hostname       TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'pending',
			exit_code      INTEGER,
			stdout         TEXT,
			stderr         TEXT,
			duration_ms    INTEGER,
			error_msg      TEXT,
			dispatched_at  DATETIME,
			finished_at    DATETIME
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB
}

// insertTaskRun inserts a task_run with explicit created_at and timeout for testing expiry.
func insertTaskRun(t *testing.T, sqlDB *sql.DB, id, status, createdAt string, timeoutSecs int) {
	t.Helper()
	_, err := sqlDB.Exec(
		`INSERT INTO task_runs (id, command, target_glob, operator, status, created_at, timeout_secs)
		 VALUES (?, 'longdrawer test', '*', 'admin', ?, ?, ?)`,
		id, status, createdAt, timeoutSecs,
	)
	if err != nil {
		t.Fatalf("insert task_run %s: %v", id, err)
	}
}

// insertTaskRunTarget inserts a task_run_target.
func insertTaskRunTarget(t *testing.T, sqlDB *sql.DB, id, taskRunID, status string) {
	t.Helper()
	_, err := sqlDB.Exec(
		`INSERT INTO task_run_targets (id, task_run_id, envoy_id, hostname, status)
		 VALUES (?, ?, 'envoy-1', 'host1.example.com', ?)`,
		id, taskRunID, status,
	)
	if err != nil {
		t.Fatalf("insert task_run_target %s: %v", id, err)
	}
}

func TestReap_NoExpiredRuns(t *testing.T) {
	sqlDB := openReaperTestDB(t)
	queries := db.New(sqlDB)
	reaper := NewReaper(queries)

	// Empty database — reap should be a no-op with no errors.
	reaper.reap(context.Background())

	// Also test with an active (non-expired) run.
	insertTaskRun(t, sqlDB, "run-active", "running", time.Now().UTC().Format("2006-01-02 15:04:05"), 3600)

	reaper.reap(context.Background())

	// Verify it's still running.
	var status string
	err := sqlDB.QueryRow("SELECT status FROM task_runs WHERE id = 'run-active'").Scan(&status)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "running" {
		t.Errorf("expected status 'running', got %q", status)
	}
}

func TestReap_ExpiredRun(t *testing.T) {
	sqlDB := openReaperTestDB(t)
	queries := db.New(sqlDB)
	reaper := NewReaper(queries)

	// Insert a task_run created 10 minutes ago with a 5-second timeout → already expired.
	pastTime := time.Now().UTC().Add(-10 * time.Minute).Format("2006-01-02 15:04:05")
	insertTaskRun(t, sqlDB, "run-expired", "running", pastTime, 5)

	// Insert pending and dispatched targets for the expired run.
	insertTaskRunTarget(t, sqlDB, "target-1", "run-expired", "pending")
	insertTaskRunTarget(t, sqlDB, "target-2", "run-expired", "dispatched")
	// Also insert a completed target that should NOT be affected.
	insertTaskRunTarget(t, sqlDB, "target-3", "run-expired", "complete")

	reaper.reap(context.Background())

	// Verify task_run status is now "timeout".
	var status string
	err := sqlDB.QueryRow("SELECT status FROM task_runs WHERE id = 'run-expired'").Scan(&status)
	if err != nil {
		t.Fatalf("query task_run: %v", err)
	}
	if status != "timeout" {
		t.Errorf("expected task_run status 'timeout', got %q", status)
	}

	// Verify pending and dispatched targets are timed out.
	for _, tc := range []struct {
		id, expected string
	}{
		{"target-1", "timeout"},
		{"target-2", "timeout"},
		{"target-3", "complete"}, // should remain unchanged
	} {
		var tStatus string
		err := sqlDB.QueryRow("SELECT status FROM task_run_targets WHERE id = ?", tc.id).Scan(&tStatus)
		if err != nil {
			t.Fatalf("query target %s: %v", tc.id, err)
		}
		if tStatus != tc.expected {
			t.Errorf("target %s: expected status %q, got %q", tc.id, tc.expected, tStatus)
		}
	}
}

func TestReap_MultipleExpiredRuns(t *testing.T) {
	sqlDB := openReaperTestDB(t)
	queries := db.New(sqlDB)
	reaper := NewReaper(queries)

	pastTime := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05")

	// Insert 3 expired runs.
	insertTaskRun(t, sqlDB, "run-1", "running", pastTime, 10)
	insertTaskRun(t, sqlDB, "run-2", "pending", pastTime, 5)
	insertTaskRun(t, sqlDB, "run-3", "running", pastTime, 1)

	// Also insert a non-expired run (should remain untouched).
	insertTaskRun(t, sqlDB, "run-active", "running", time.Now().UTC().Format("2006-01-02 15:04:05"), 3600)

	// Insert targets for each expired run.
	insertTaskRunTarget(t, sqlDB, "t1", "run-1", "pending")
	insertTaskRunTarget(t, sqlDB, "t2", "run-2", "dispatched")
	insertTaskRunTarget(t, sqlDB, "t3", "run-3", "pending")

	reaper.reap(context.Background())

	// Verify all expired runs are timed out.
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		var status string
		err := sqlDB.QueryRow("SELECT status FROM task_runs WHERE id = ?", id).Scan(&status)
		if err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if status != "timeout" {
			t.Errorf("%s: expected status 'timeout', got %q", id, status)
		}
	}

	// Verify active run is still running.
	var activeStatus string
	err := sqlDB.QueryRow("SELECT status FROM task_runs WHERE id = 'run-active'").Scan(&activeStatus)
	if err != nil {
		t.Fatalf("query active: %v", err)
	}
	if activeStatus != "running" {
		t.Errorf("active run: expected 'running', got %q", activeStatus)
	}

	// Verify all expired targets are timed out.
	for _, id := range []string{"t1", "t2", "t3"} {
		var tStatus string
		err := sqlDB.QueryRow("SELECT status FROM task_run_targets WHERE id = ?", id).Scan(&tStatus)
		if err != nil {
			t.Fatalf("query target %s: %v", id, err)
		}
		if tStatus != "timeout" {
			t.Errorf("target %s: expected 'timeout', got %q", id, tStatus)
		}
	}
}

func TestReap_AlreadyCompleteRun_NotAffected(t *testing.T) {
	sqlDB := openReaperTestDB(t)
	queries := db.New(sqlDB)
	reaper := NewReaper(queries)

	// Insert runs that are old but already complete/failed — should not be reaped.
	pastTime := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	insertTaskRun(t, sqlDB, "run-complete", "complete", pastTime, 5)
	insertTaskRun(t, sqlDB, "run-failed", "failed", pastTime, 5)

	reaper.reap(context.Background())

	for _, tc := range []struct {
		id, expected string
	}{
		{"run-complete", "complete"},
		{"run-failed", "failed"},
	} {
		var status string
		err := sqlDB.QueryRow("SELECT status FROM task_runs WHERE id = ?", tc.id).Scan(&status)
		if err != nil {
			t.Fatalf("query %s: %v", tc.id, err)
		}
		if status != tc.expected {
			t.Errorf("%s: expected status %q, got %q", tc.id, tc.expected, status)
		}
	}
}

func TestReaper_Start_ContextCancellation(t *testing.T) {
	sqlDB := openReaperTestDB(t)
	queries := db.New(sqlDB)
	reaper := NewReaper(queries)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		reaper.Start(ctx)
		close(done)
	}()

	// Cancel immediately — the goroutine should exit promptly.
	cancel()

	select {
	case <-done:
		// Success — goroutine exited.
	case <-time.After(5 * time.Second):
		t.Fatal("reaper goroutine did not exit within 5 seconds after context cancellation")
	}
}

func TestReap_SetsFinishedAt(t *testing.T) {
	sqlDB := openReaperTestDB(t)
	queries := db.New(sqlDB)
	reaper := NewReaper(queries)

	pastTime := time.Now().UTC().Add(-10 * time.Minute).Format("2006-01-02 15:04:05")
	insertTaskRun(t, sqlDB, "run-check-ts", "running", pastTime, 5)

	reaper.reap(context.Background())

	// Verify finished_at is set.
	var finishedAt sql.NullString
	err := sqlDB.QueryRow("SELECT finished_at FROM task_runs WHERE id = 'run-check-ts'").Scan(&finishedAt)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !finishedAt.Valid || finishedAt.String == "" {
		t.Error("expected finished_at to be set after timeout")
	}
}
