// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package workflows

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
)

func fastPoll(t *testing.T) {
	t.Helper()
	old := pollInterval
	pollInterval = 5 * time.Millisecond
	t.Cleanup(func() { pollInterval = old })
}

func insertTaskRun(t *testing.T, sqlDB *sql.DB, id, status string, timeoutSecs int, createdAgo time.Duration) {
	t.Helper()
	created := time.Now().UTC().Add(-createdAgo).Format("2006-01-02 15:04:05")
	if _, err := sqlDB.Exec(
		`INSERT INTO task_runs (id, command, target_glob, operator, status, timeout_secs, created_at) VALUES (?, 'longdrawer test', '*', 'test', ?, ?, ?)`,
		id, status, timeoutSecs, created,
	); err != nil {
		t.Fatalf("insert task_run: %v", err)
	}
}

func newWorkflowRun(t *testing.T, q *db.Queries, name string, steps int) string {
	t.Helper()
	run, err := q.CreateWorkflowRun(context.Background(), db.CreateWorkflowRunParams{
		WorkflowName: name, TotalSteps: int64(steps), Operator: "admin",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return run.ID
}

// An abort that lands while a step's task run is rolling must cancel that
// task run and record the workflow as aborted — never take on_failure,
// never record failed.
func TestExecuteWorkflow_AbortDuringPoll_CancelsTaskRunAndRecordsAborted(t *testing.T) {
	fastPoll(t)
	sqlDB := openExecTestDB(t)
	q := db.New(sqlDB)
	runner := &mockTaskRunner{results: []mockPushResult{
		{result: &isgrpc.DispatchResult{ID: "push-rolling", Status: "running"}},
	}}
	engine := NewEngine(q, runner, nil)
	t.Cleanup(engine.Close)
	insertTaskRun(t, sqlDB, "push-rolling", "running", 3600, 0)

	def := &WorkflowDef{Name: "deploy", Steps: []StepDef{
		{Name: "step1", Command: "longdrawer step1", Target: "*", OnFailure: "recover"},
		{Name: "recover", Command: "longdrawer recover", Target: "*"},
	}}
	runID := newWorkflowRun(t, q, "deploy", 2)
	engine.liveRuns.Store(runID, struct{}{})

	done := make(chan struct{})
	go func() { engine.executeWorkflow(runID, def); close(done) }()
	time.Sleep(50 * time.Millisecond) // let it reach the poll loop
	engine.Abort(runID)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow did not stop after abort")
	}

	wf, err := q.GetWorkflowRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Status != "aborted" || wf.FailureReason.String != "operator_abort" {
		t.Errorf("workflow status = %q reason %q, want aborted/operator_abort", wf.Status, wf.FailureReason.String)
	}
	if wf.CurrentStep != 0 {
		t.Errorf("current_step = %d, want 0 (the step that was aborted, not its on_failure target)", wf.CurrentStep)
	}
	if n := runner.callCount(); n != 1 {
		t.Errorf("dispatch calls = %d, want 1 (abort must not take on_failure)", n)
	}
	tr, err := q.GetTaskRun(context.Background(), "push-rolling")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != "cancelled" {
		t.Errorf("task run status after abort = %q, want cancelled", tr.Status)
	}
}

// A dispatch that returns no task run ID must fail the step, not poll forever.
func TestExecuteWorkflow_EmptyTaskRunID_FailsStep(t *testing.T) {
	fastPoll(t)
	sqlDB := openExecTestDB(t)
	q := db.New(sqlDB)
	runner := &mockTaskRunner{results: []mockPushResult{
		{result: &isgrpc.DispatchResult{ID: "", Status: "running"}},
	}}
	engine := NewEngine(q, runner, nil)
	t.Cleanup(engine.Close)
	def := &WorkflowDef{Name: "w", Steps: []StepDef{{Name: "s", Command: "longdrawer s", Target: "*"}}}
	runID := newWorkflowRun(t, q, "w", 1)

	done := make(chan struct{})
	go func() { engine.executeWorkflow(runID, def); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow hung polling an empty task run ID")
	}
	wf, err := q.GetWorkflowRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Status != "failed" || !strings.Contains(wf.FailureReason.String, "no task run id") {
		t.Errorf("status = %q reason = %q, want failed / no task run id", wf.Status, wf.FailureReason.String)
	}
}

// Persistent GetTaskRun errors exhaust a budget and fail the step.
func TestPollPushCompletion_ErrorBudget(t *testing.T) {
	fastPoll(t)
	engine := NewEngine(db.New(openExecTestDB(t)), &mockTaskRunner{}, nil)
	t.Cleanup(engine.Close)

	got := make(chan string, 1)
	go func() { got <- engine.pollPushCompletion(context.Background(), "wf", "no-such-run") }()
	select {
	case s := <-got:
		if s != stepFailed {
			t.Errorf("status = %q, want %q", s, stepFailed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pollPushCompletion looped forever on a persistent GetTaskRun error")
	}
}

// A task run still pending well past created_at + timeout_secs is cancelled
// and the step reports timeout.
func TestPollPushCompletion_DeadlineCancelsTaskRun(t *testing.T) {
	fastPoll(t)
	sqlDB := openExecTestDB(t)
	q := db.New(sqlDB)
	engine := NewEngine(q, &mockTaskRunner{}, nil)
	t.Cleanup(engine.Close)
	insertTaskRun(t, sqlDB, "stuck", "running", 10, time.Hour)

	got := make(chan string, 1)
	go func() { got <- engine.pollPushCompletion(context.Background(), "wf", "stuck") }()
	select {
	case s := <-got:
		if s != stepTimeout {
			t.Errorf("status = %q, want %q", s, stepTimeout)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pollPushCompletion never gave up on a run past its deadline")
	}
	tr, err := q.GetTaskRun(context.Background(), "stuck")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != "cancelled" {
		t.Errorf("task run status = %q, want cancelled", tr.Status)
	}
}

// Without an on_failure branch an abort mid-poll used to fall through to
// failWorkflow and be recorded as failed.
func TestExecuteWorkflow_AbortDuringPoll_NoOnFailure_RecordsAborted(t *testing.T) {
	fastPoll(t)
	sqlDB := openExecTestDB(t)
	q := db.New(sqlDB)
	runner := &mockTaskRunner{results: []mockPushResult{
		{result: &isgrpc.DispatchResult{ID: "push-r2", Status: "running"}},
	}}
	engine := NewEngine(q, runner, nil)
	t.Cleanup(engine.Close)
	insertTaskRun(t, sqlDB, "push-r2", "running", 3600, 0)
	def := &WorkflowDef{Name: "d", Steps: []StepDef{{Name: "s", Command: "longdrawer s", Target: "*"}}}
	runID := newWorkflowRun(t, q, "d", 1)
	engine.liveRuns.Store(runID, struct{}{})

	done := make(chan struct{})
	go func() { engine.executeWorkflow(runID, def); close(done) }()
	time.Sleep(50 * time.Millisecond)
	engine.Abort(runID)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow did not stop after abort")
	}
	wf, err := q.GetWorkflowRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Status != "aborted" {
		t.Errorf("workflow status = %q, want aborted", wf.Status)
	}
}
