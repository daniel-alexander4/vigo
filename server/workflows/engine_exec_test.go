// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package workflows

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/db"
	isgrpc "github.com/daniel-alexander4/vigo/server/grpc"
)

// mockPushRunner records DispatchPush calls and returns configurable results.
type mockPushRunner struct {
	mu      sync.Mutex
	calls   []isgrpc.DispatchParams
	results []mockPushResult
	idx     int
}

type mockPushResult struct {
	result *isgrpc.DispatchResult
	err    error
}

func (m *mockPushRunner) DispatchPush(_ context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, params)
	if m.idx < len(m.results) {
		r := m.results[m.idx]
		m.idx++
		return r.result, r.err
	}
	return &isgrpc.DispatchResult{ID: fmt.Sprintf("push-%d", m.idx), Status: "running"}, nil
}

func (m *mockPushRunner) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockPushRunner) getCall(i int) isgrpc.DispatchParams {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[i]
}

// openExecTestDB creates an in-memory SQLite database with workflow + push tables.
func openExecTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE push_runs (
			id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6)))),
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
		CREATE TABLE workflow_runs (
			id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6)))),
			workflow_name  TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'pending',
			current_step   INTEGER NOT NULL DEFAULT 0,
			total_steps    INTEGER NOT NULL DEFAULT 0,
			operator       TEXT NOT NULL DEFAULT '',
			created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
			started_at     DATETIME,
			finished_at    DATETIME,
			failure_reason TEXT
		);
		CREATE TABLE workflow_step_results (
			id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6)))),
			workflow_run_id TEXT NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
			step_index      INTEGER NOT NULL DEFAULT 0,
			step_name       TEXT NOT NULL DEFAULT '',
			push_run_id     TEXT,
			status          TEXT NOT NULL DEFAULT 'pending',
			started_at      DATETIME,
			finished_at     DATETIME
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB
}

// insertPushRunWithStatus inserts a push_run with the given ID and status.
func insertPushRunWithStatus(t *testing.T, sqlDB *sql.DB, id, status string) {
	t.Helper()
	_, err := sqlDB.Exec(
		`INSERT INTO push_runs (id, command, target_glob, operator, status) VALUES (?, 'echo test', '*', 'test', ?)`,
		id, status,
	)
	if err != nil {
		t.Fatalf("insert push_run: %v", err)
	}
}

func TestRunWorkflow_UnknownWorkflow(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)
	engine := NewEngine(queries, nil)

	_, err := engine.RunWorkflow(context.Background(), "nonexistent", "admin")
	if err == nil {
		t.Fatal("expected error for unknown workflow")
	}
	if err.Error() != "unknown workflow: nonexistent" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunWorkflow_CreatesDBRecord(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			{result: &isgrpc.DispatchResult{ID: "push-1", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)
	engine.mu.Lock()
	engine.workflows["test-wf"] = &WorkflowDef{
		Name: "test-wf",
		Steps: []StepDef{
			{Name: "step1", Command: "echo hello", Target: "*"},
		},
	}
	engine.mu.Unlock()

	// Pre-insert a completed push_run so pollPushCompletion can find it.
	insertPushRunWithStatus(t, sqlDB, "push-1", "complete")

	runID, err := engine.RunWorkflow(context.Background(), "test-wf", "admin")
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if runID == "" {
		t.Fatal("expected non-empty run ID")
	}

	// Wait for the background goroutine to complete (poll ticker is 2s).
	time.Sleep(3 * time.Second)

	// Verify the workflow_runs row was created.
	row := sqlDB.QueryRow("SELECT workflow_name, operator, total_steps FROM workflow_runs WHERE id = ?", runID)
	var name, operator string
	var totalSteps int64
	if err := row.Scan(&name, &operator, &totalSteps); err != nil {
		t.Fatalf("scan workflow_runs: %v", err)
	}
	if name != "test-wf" {
		t.Errorf("expected workflow_name 'test-wf', got %q", name)
	}
	if operator != "admin" {
		t.Errorf("expected operator 'admin', got %q", operator)
	}
	if totalSteps != 1 {
		t.Errorf("expected total_steps 1, got %d", totalSteps)
	}
}

func TestExecuteWorkflow_CompletesAllSteps(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			{result: &isgrpc.DispatchResult{ID: "push-1", Status: "running"}, err: nil},
			{result: &isgrpc.DispatchResult{ID: "push-2", Status: "running"}, err: nil},
			{result: &isgrpc.DispatchResult{ID: "push-3", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "deploy",
		Steps: []StepDef{
			{Name: "setup", Command: "echo setup", Target: "*.web.*"},
			{Name: "deploy", Command: "echo deploy", Target: "*.web.*"},
			{Name: "verify", Command: "echo verify", Target: "*.web.*"},
		},
	}
	engine.mu.Lock()
	engine.workflows["deploy"] = def
	engine.mu.Unlock()

	// Pre-insert push_runs as complete so pollPushCompletion returns immediately.
	insertPushRunWithStatus(t, sqlDB, "push-1", "complete")
	insertPushRunWithStatus(t, sqlDB, "push-2", "complete")
	insertPushRunWithStatus(t, sqlDB, "push-3", "complete")

	// Create the workflow run record.
	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "deploy",
		TotalSteps:   3,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	// Execute synchronously (not in goroutine).
	engine.executeWorkflow(run.ID, def)

	// Verify workflow completed.
	wfRun, err := queries.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if wfRun.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", wfRun.Status)
	}
	if wfRun.CurrentStep != 3 {
		t.Errorf("expected current_step 3, got %d", wfRun.CurrentStep)
	}
	if !wfRun.FinishedAt.Valid {
		t.Error("expected finished_at to be set")
	}

	// Verify all 3 dispatch calls were made.
	if runner.callCount() != 3 {
		t.Errorf("expected 3 dispatch calls, got %d", runner.callCount())
	}

	// Verify step results exist.
	rows, err := sqlDB.Query("SELECT step_name, status FROM workflow_step_results WHERE workflow_run_id = ? ORDER BY step_index", run.ID)
	if err != nil {
		t.Fatalf("query step results: %v", err)
	}
	defer rows.Close()

	var steps []struct{ name, status string }
	for rows.Next() {
		var s struct{ name, status string }
		if err := rows.Scan(&s.name, &s.status); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, s)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 step results, got %d", len(steps))
	}
	for i, s := range steps {
		if s.status != "complete" {
			t.Errorf("step %d (%s): expected status 'complete', got %q", i, s.name, s.status)
		}
	}
}

func TestExecuteWorkflow_StepFailure_OnFailureBranch(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			// First step dispatch fails.
			{result: nil, err: fmt.Errorf("dispatch error: host unreachable")},
			// The on_failure branch (rollback) succeeds.
			{result: &isgrpc.DispatchResult{ID: "push-rollback", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "deploy",
		Steps: []StepDef{
			{Name: "deploy", Command: "echo deploy", Target: "*.web.*", OnFailure: "rollback"},
			{Name: "verify", Command: "echo verify", Target: "*.web.*"},
			{Name: "rollback", Command: "echo rollback", Target: "*.web.*"},
		},
	}
	engine.mu.Lock()
	engine.workflows["deploy"] = def
	engine.mu.Unlock()

	// Pre-insert push_run for rollback step.
	insertPushRunWithStatus(t, sqlDB, "push-rollback", "complete")

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "deploy",
		TotalSteps:   3,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	engine.executeWorkflow(run.ID, def)

	// Verify that the runner was called twice: deploy (failed) then rollback.
	if runner.callCount() != 2 {
		t.Fatalf("expected 2 dispatch calls, got %d", runner.callCount())
	}

	// First call should be the deploy step.
	call0 := runner.getCall(0)
	if call0.Command != "echo deploy" {
		t.Errorf("expected first call command 'echo deploy', got %q", call0.Command)
	}

	// Second call should be the rollback step.
	call1 := runner.getCall(1)
	if call1.Command != "echo rollback" {
		t.Errorf("expected second call command 'echo rollback', got %q", call1.Command)
	}

	// After rollback step completes, the workflow ends (rollback is the last step at idx 2,
	// and there is no more step after it), so the workflow should be complete.
	wfRun, err := queries.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if wfRun.Status != "complete" {
		t.Errorf("expected status 'complete' (after rollback succeeds), got %q", wfRun.Status)
	}
}

func TestExecuteWorkflow_StepFailure_NoOnFailure_WorkflowFails(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			{result: nil, err: fmt.Errorf("dispatch failed")},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "simple",
		Steps: []StepDef{
			{Name: "step1", Command: "echo step1", Target: "*"},
		},
	}
	engine.mu.Lock()
	engine.workflows["simple"] = def
	engine.mu.Unlock()

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "simple",
		TotalSteps:   1,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	engine.executeWorkflow(run.ID, def)

	wfRun, err := queries.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if wfRun.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", wfRun.Status)
	}
	if !wfRun.FailureReason.Valid || wfRun.FailureReason.String == "" {
		t.Error("expected failure_reason to be set")
	}
}

func TestExecuteWorkflow_RespectsAbortFlag(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{}
	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "deploy",
		Steps: []StepDef{
			{Name: "step1", Command: "echo step1", Target: "*"},
			{Name: "step2", Command: "echo step2", Target: "*"},
		},
	}
	engine.mu.Lock()
	engine.workflows["deploy"] = def
	engine.mu.Unlock()

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "deploy",
		TotalSteps:   2,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	// Set abort flag before execution starts.
	engine.Abort(run.ID)

	engine.executeWorkflow(run.ID, def)

	// Verify workflow was aborted.
	wfRun, err := queries.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if wfRun.Status != "aborted" {
		t.Errorf("expected status 'aborted', got %q", wfRun.Status)
	}
	if !wfRun.FailureReason.Valid || wfRun.FailureReason.String != "operator_abort" {
		t.Errorf("expected failure_reason 'operator_abort', got %q", wfRun.FailureReason.String)
	}

	// Verify no dispatch calls were made.
	if runner.callCount() != 0 {
		t.Errorf("expected 0 dispatch calls, got %d", runner.callCount())
	}

	// Verify abort flag was cleaned up.
	if engine.isAborted(run.ID) {
		t.Error("expected abort flag to be cleaned up after execution")
	}
}

func TestRecoverInterrupted_MarksRunningAsFailed(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	// Insert workflow runs in various states.
	for _, tc := range []struct {
		id, status string
	}{
		{"run-1", "running"},
		{"run-2", "pending"},
		{"run-3", "complete"},
		{"run-4", "failed"},
	} {
		_, err := sqlDB.Exec(
			`INSERT INTO workflow_runs (id, workflow_name, operator, status, total_steps) VALUES (?, 'test', 'admin', ?, 1)`,
			tc.id, tc.status,
		)
		if err != nil {
			t.Fatalf("insert workflow run %s: %v", tc.id, err)
		}
	}

	// Run recovery.
	if err := RecoverInterrupted(context.Background(), queries); err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}

	// Verify running and pending are now failed.
	for _, tc := range []struct {
		id, expected string
	}{
		{"run-1", "failed"},
		{"run-2", "failed"},
		{"run-3", "complete"},
		{"run-4", "failed"},
	} {
		var status string
		err := sqlDB.QueryRow("SELECT status FROM workflow_runs WHERE id = ?", tc.id).Scan(&status)
		if err != nil {
			t.Fatalf("query %s: %v", tc.id, err)
		}
		if status != tc.expected {
			t.Errorf("run %s: expected status %q, got %q", tc.id, tc.expected, status)
		}
	}

	// Verify failure_reason is set on recovered runs.
	for _, id := range []string{"run-1", "run-2"} {
		var reason sql.NullString
		err := sqlDB.QueryRow("SELECT failure_reason FROM workflow_runs WHERE id = ?", id).Scan(&reason)
		if err != nil {
			t.Fatalf("query failure_reason %s: %v", id, err)
		}
		if !reason.Valid || reason.String != "restart_interrupted" {
			t.Errorf("run %s: expected failure_reason 'restart_interrupted', got %q", id, reason.String)
		}
	}
}

func TestExecuteWorkflow_OnSuccessBranching(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			// "check" step succeeds → on_success branches to "notify" (skips "deploy").
			{result: &isgrpc.DispatchResult{ID: "push-check", Status: "running"}, err: nil},
			// "notify" step succeeds.
			{result: &isgrpc.DispatchResult{ID: "push-notify", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "branch",
		Steps: []StepDef{
			{Name: "check", Command: "echo check", Target: "*", OnSuccess: "notify"},
			{Name: "deploy", Command: "echo deploy", Target: "*"},
			{Name: "notify", Command: "echo notify", Target: "*"},
		},
	}
	engine.mu.Lock()
	engine.workflows["branch"] = def
	engine.mu.Unlock()

	// Pre-insert push_runs.
	insertPushRunWithStatus(t, sqlDB, "push-check", "complete")
	insertPushRunWithStatus(t, sqlDB, "push-notify", "complete")

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "branch",
		TotalSteps:   3,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	engine.executeWorkflow(run.ID, def)

	// Only 2 dispatch calls: check and notify. "deploy" is skipped.
	if runner.callCount() != 2 {
		t.Fatalf("expected 2 dispatch calls, got %d", runner.callCount())
	}

	call0 := runner.getCall(0)
	if call0.Command != "echo check" {
		t.Errorf("expected first call 'echo check', got %q", call0.Command)
	}
	call1 := runner.getCall(1)
	if call1.Command != "echo notify" {
		t.Errorf("expected second call 'echo notify', got %q", call1.Command)
	}

	// Workflow should be complete (notify is last step at idx 2, next would be idx 3 which exits loop).
	wfRun, err := queries.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if wfRun.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", wfRun.Status)
	}
}

func TestExecuteWorkflow_PollPushCompletion_Failed(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			{result: &isgrpc.DispatchResult{ID: "push-fail", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "failing",
		Steps: []StepDef{
			{Name: "step1", Command: "echo step1", Target: "*"},
		},
	}
	engine.mu.Lock()
	engine.workflows["failing"] = def
	engine.mu.Unlock()

	// Pre-insert push_run as failed so pollPushCompletion returns "failed".
	insertPushRunWithStatus(t, sqlDB, "push-fail", "failed")

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "failing",
		TotalSteps:   1,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	engine.executeWorkflow(run.ID, def)

	wfRun, err := queries.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if wfRun.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", wfRun.Status)
	}
}

func TestExecuteWorkflow_PollFailure_OnFailureBranch(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			// First step dispatches successfully but push run will be found as "failed".
			{result: &isgrpc.DispatchResult{ID: "push-1", Status: "running"}, err: nil},
			// On-failure branch (cleanup) dispatches successfully.
			{result: &isgrpc.DispatchResult{ID: "push-cleanup", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "deploy-with-cleanup",
		Steps: []StepDef{
			{Name: "deploy", Command: "echo deploy", Target: "*", OnFailure: "cleanup"},
			{Name: "cleanup", Command: "echo cleanup", Target: "*"},
		},
	}
	engine.mu.Lock()
	engine.workflows["deploy-with-cleanup"] = def
	engine.mu.Unlock()

	// Push 1 failed, push cleanup succeeded.
	insertPushRunWithStatus(t, sqlDB, "push-1", "failed")
	insertPushRunWithStatus(t, sqlDB, "push-cleanup", "complete")

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "deploy-with-cleanup",
		TotalSteps:   2,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	engine.executeWorkflow(run.ID, def)

	// Both steps should have been dispatched.
	if runner.callCount() != 2 {
		t.Fatalf("expected 2 dispatch calls, got %d", runner.callCount())
	}

	// Workflow should complete because cleanup succeeds and is the last step.
	wfRun, err := queries.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if wfRun.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", wfRun.Status)
	}
}

func TestExecuteWorkflow_TaskParams(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			{result: &isgrpc.DispatchResult{ID: "push-task", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "task-wf",
		Steps: []StepDef{
			{
				Name:    "restart",
				Task:    "restart-app",
				Target:  "*.web.*",
				Params:  map[string]string{"env": "staging", "force": "true"},
				Timeout: 120,
			},
		},
	}
	engine.mu.Lock()
	engine.workflows["task-wf"] = def
	engine.mu.Unlock()

	insertPushRunWithStatus(t, sqlDB, "push-task", "complete")

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "task-wf",
		TotalSteps:   1,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	engine.executeWorkflow(run.ID, def)

	if runner.callCount() != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", runner.callCount())
	}

	call := runner.getCall(0)
	if call.TaskName != "restart-app" {
		t.Errorf("expected TaskName 'restart-app', got %q", call.TaskName)
	}
	if call.TargetSpec != "*.web.*" {
		t.Errorf("expected TargetSpec '*.web.*', got %q", call.TargetSpec)
	}
	if call.Params["env"] != "staging" {
		t.Errorf("expected param env=staging, got %q", call.Params["env"])
	}
	if call.Params["force"] != "true" {
		t.Errorf("expected param force=true, got %q", call.Params["force"])
	}
	if call.TimeoutSecs != 120 {
		t.Errorf("expected TimeoutSecs 120, got %d", call.TimeoutSecs)
	}
	if call.Operator != "workflow:"+run.ID {
		t.Errorf("expected Operator 'workflow:%s', got %q", run.ID, call.Operator)
	}
}

func TestExecuteWorkflow_RollingParams(t *testing.T) {
	sqlDB := openExecTestDB(t)
	queries := db.New(sqlDB)

	runner := &mockPushRunner{
		results: []mockPushResult{
			{result: &isgrpc.DispatchResult{ID: "push-rolling", Status: "running"}, err: nil},
		},
	}

	engine := NewEngine(queries, runner)

	def := &WorkflowDef{
		Name: "rolling-wf",
		Steps: []StepDef{
			{
				Name:        "deploy",
				Command:     "echo deploy",
				Target:      "*.web.*",
				BatchSize:   "25%",
				MaxFailures: "2",
				HealthCheck: "curl -s http://localhost/health",
			},
		},
	}
	engine.mu.Lock()
	engine.workflows["rolling-wf"] = def
	engine.mu.Unlock()

	insertPushRunWithStatus(t, sqlDB, "push-rolling", "complete")

	run, err := queries.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: "rolling-wf",
		TotalSteps:   1,
		Operator:     "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	engine.executeWorkflow(run.ID, def)

	call := runner.getCall(0)
	if call.BatchSize != "25%" {
		t.Errorf("expected BatchSize '25%%', got %q", call.BatchSize)
	}
	if call.MaxFailures != "2" {
		t.Errorf("expected MaxFailures '2', got %q", call.MaxFailures)
	}
	if call.HealthCheck != "curl -s http://localhost/health" {
		t.Errorf("expected HealthCheck 'curl -s http://localhost/health', got %q", call.HealthCheck)
	}
}
