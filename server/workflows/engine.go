// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package workflows

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/validate"
)

// StepDef defines a single workflow step.
type StepDef struct {
	Name        string            `yaml:"name"`
	Task        string            `yaml:"task"`    // named task reference
	Command     string            `yaml:"command"` // ad-hoc command (alternative to task)
	Target      string            `yaml:"target"`
	Params      map[string]string `yaml:"params"`
	Timeout     int32             `yaml:"timeout"`
	BatchSize   string            `yaml:"batch_size"`
	MaxFailures string            `yaml:"max_failures"`
	HealthCheck string            `yaml:"health_check"`
	OnSuccess   string            `yaml:"on_success"` // step name or "" (next)
	OnFailure   string            `yaml:"on_failure"` // step name or "" (abort)
}

// TriggerDef defines when a workflow should auto-execute based on trait changes.
type TriggerDef struct {
	On       string `yaml:"on"`        // "trait_change"
	TraitKey string `yaml:"trait_key"` // flattened trait key (e.g., "security_scan.chkrootkit.infected")
	Value    string `yaml:"value"`     // trigger when trait changes TO this value (empty = any change)
}

// WorkflowDef is a reusable workflow definition loaded from a YAML file.
type WorkflowDef struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Trigger     *TriggerDef `yaml:"trigger"`
	Steps       []StepDef   `yaml:"steps"`
}

// TaskRunner dispatches push commands and returns results.
type TaskRunner interface {
	DispatchPush(ctx context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error)
}

// Engine executes workflows.
//
// Goroutine lifecycle is bound to the Engine: every RunWorkflow spawns
// an executeWorkflow goroutine under e.ctx and e.wg. Close() cancels
// e.ctx and waits for all in-flight goroutines to drain before
// returning — callers (and tests) must Close() before tearing down
// the DB the Engine reads from, or an orphan executeWorkflow goroutine
// will race the shutdown and panic on a closed *sql.DB.
type Engine struct {
	queries    *db.Queries
	runner     TaskRunner
	metrics    *metrics.Metrics
	workflows  map[string]*WorkflowDef
	mu         sync.RWMutex
	abortFlags sync.Map // workflowRunID → struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewEngine creates a workflow engine. m owns the workflow-run counters
// and may be nil in tests that don't observe those metrics.
//
// The Engine owns its goroutine context — call Close() (typically via
// t.Cleanup in tests, graceful shutdown in main) to cancel in-flight
// executeWorkflow goroutines and wait for them to drain.
func NewEngine(queries *db.Queries, runner TaskRunner, m *metrics.Metrics) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		queries:   queries,
		runner:    runner,
		metrics:   m,
		workflows: make(map[string]*WorkflowDef),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Close cancels the Engine's goroutine context and waits for all
// in-flight executeWorkflow goroutines to drain. Safe to call multiple
// times — context.CancelFunc is idempotent.
//
// Must be called before tearing down the DB the Engine reads from;
// otherwise an orphan workflow goroutine can race the shutdown and
// panic on a closed *sql.DB at engine.go:308 (DispatchPush).
func (e *Engine) Close() {
	e.cancel()
	e.wg.Wait()
}

// Load reads all workflow YAML files from a directory.
func (e *Engine) Load(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading workflows directory: %w", err)
	}

	workflows := make(map[string]*WorkflowDef)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".vgo" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		var def WorkflowDef
		if err := yaml.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("parsing %s: %w", entry.Name(), err)
		}

		if def.Name == "" {
			def.Name = strings.TrimSuffix(entry.Name(), ext)
		}
		if len(def.Steps) == 0 {
			return fmt.Errorf("workflow %q has no steps", def.Name)
		}

		// Self-protection guardrails — refuse to load a workflow whose
		// ad-hoc command steps would be blocked at dispatch. The
		// agent's hardcoded guardrails are the final gate; surfacing
		// the block at workflow-load makes the failure visible at
		// publish time instead of at runtime. Steps that reference a
		// named task (step.Task) are validated at task-store load.
		for i, step := range def.Steps {
			if step.Command == "" {
				continue
			}
			if err := validate.ValidatePushCommand(step.Command); err != nil {
				return fmt.Errorf("workflow %q step %d (%s): %w", def.Name, i+1, step.Name, err)
			}
		}

		workflows[def.Name] = &def
	}

	e.mu.Lock()
	e.workflows = workflows
	e.mu.Unlock()
	return nil
}

// Get returns a workflow definition by name.
func (e *Engine) Get(name string) (*WorkflowDef, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	w, ok := e.workflows[name]
	return w, ok
}

// List returns all workflow names sorted alphabetically.
func (e *Engine) List() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.workflows))
	for name := range e.workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TriggeredWorkflows returns workflow names that should fire based on trait changes.
// oldTraits and newTraits are flattened key→value maps.
func (e *Engine) TriggeredWorkflows(oldTraits, newTraits map[string]string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var triggered []string
	for name, def := range e.workflows {
		if def.Trigger == nil || def.Trigger.On != "trait_change" {
			continue
		}
		key := def.Trigger.TraitKey
		oldVal := oldTraits[key]
		newVal := newTraits[key]

		if oldVal == newVal {
			continue // no change
		}

		// If trigger specifies a target value, only fire when new value matches.
		if def.Trigger.Value != "" && newVal != def.Trigger.Value {
			continue
		}

		triggered = append(triggered, name)
	}
	sort.Strings(triggered)
	return triggered
}

// RunWorkflow starts a workflow execution in a background goroutine.
// Returns the workflow run ID immediately.
func (e *Engine) RunWorkflow(ctx context.Context, name, operator string) (string, error) {
	def, ok := e.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown workflow: %s", name)
	}

	run, err := e.queries.CreateWorkflowRun(ctx, db.CreateWorkflowRunParams{
		WorkflowName: name,
		TotalSteps:   int64(len(def.Steps)),
		Operator:     operator,
	})
	if err != nil {
		return "", fmt.Errorf("creating workflow run: %w", err)
	}

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.executeWorkflow(run.ID, def)
	}()
	return run.ID, nil
}

// Abort signals a running workflow to stop before the next step.
func (e *Engine) Abort(workflowRunID string) {
	e.abortFlags.Store(workflowRunID, struct{}{})
}

// isAborted checks if a workflow has been flagged for abort.
func (e *Engine) isAborted(workflowRunID string) bool {
	_, ok := e.abortFlags.Load(workflowRunID)
	return ok
}

// executeWorkflow runs the workflow steps sequentially.
//
// Uses e.ctx so Close() cancels the goroutine. The DB calls all take
// ctx, so cancelling mid-step returns sql.ErrConnDone / context.Canceled
// which the existing persistErr() / explicit-error paths handle cleanly.
// The top-of-loop ctx.Err() check exits before starting a new step.
func (e *Engine) executeWorkflow(runID string, def *WorkflowDef) {
	ctx := e.ctx
	defer e.abortFlags.Delete(runID)

	now := sql.NullTime{Time: time.Now(), Valid: true}
	if err := e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		ID:        runID,
		Status:    "running",
		StartedAt: now,
	}); err != nil {
		slog.Error("workflow: failed to mark running", "workflow_run_id", runID, "error", err)
		return
	}

	// Build step index for on_success/on_failure branching.
	stepIndex := make(map[string]int, len(def.Steps))
	for i, step := range def.Steps {
		stepIndex[step.Name] = i
	}

	currentIdx := 0
	for currentIdx < len(def.Steps) {
		step := def.Steps[currentIdx]

		// Engine.Close() cancellation — exit cleanly before the next
		// step's DB writes. Without this short-circuit, an Engine that
		// shuts down mid-workflow keeps executing steps until each
		// individual DB call fails — wasting work and emitting noise.
		if ctx.Err() != nil {
			return
		}

		// Check abort flag before dispatching.
		if e.isAborted(runID) {
			slog.Info("workflow: aborted", "workflow_run_id", runID, "at_step", step.Name)
			now = sql.NullTime{Time: time.Now(), Valid: true}
			persistErr(runID, e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
				ID:            runID,
				Status:        "aborted",
				CurrentStep:   int64(currentIdx),
				FinishedAt:    now,
				FailureReason: sql.NullString{String: "operator_abort", Valid: true},
			}))
			return
		}

		// Update current step.
		persistErr(runID, e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
			ID:          runID,
			Status:      "running",
			CurrentStep: int64(currentIdx),
			StartedAt:   now,
		}))

		// Create step result record.
		stepResult, err := e.queries.CreateWorkflowStepResult(ctx, db.CreateWorkflowStepResultParams{
			WorkflowRunID: runID,
			StepIndex:     int64(currentIdx),
			StepName:      step.Name,
		})
		if err != nil {
			slog.Error("workflow: failed to create step result", "workflow_run_id", runID, "step", step.Name, "error", err)
			e.failWorkflow(ctx, runID, currentIdx, fmt.Sprintf("failed to create step result: %v", err))
			return
		}

		// Mark step as running.
		stepNow := sql.NullTime{Time: time.Now(), Valid: true}
		persistErr(runID, e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
			ID:        stepResult.ID,
			Status:    "running",
			StartedAt: stepNow,
		}))

		// Build dispatch params.
		params := isgrpc.DispatchParams{
			Command:     step.Command,
			TargetSpec:  step.Target,
			Operator:    "workflow:" + runID,
			TimeoutSecs: step.Timeout,
			BatchSize:   step.BatchSize,
			MaxFailures: step.MaxFailures,
			HealthCheck: step.HealthCheck,
		}
		if step.Task != "" {
			params.TaskName = step.Task
			params.Params = step.Params
		}

		// Dispatch.
		result, err := e.runner.DispatchPush(ctx, params)
		if err != nil {
			stepFinished := sql.NullTime{Time: time.Now(), Valid: true}
			persistErr(runID, e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
				ID:         stepResult.ID,
				Status:     "failed",
				FinishedAt: stepFinished,
			}))
			if step.OnFailure != "" {
				if idx, ok := stepIndex[step.OnFailure]; ok {
					currentIdx = idx
					continue
				}
			}
			e.failWorkflow(ctx, runID, currentIdx, fmt.Sprintf("step %s dispatch failed: %v", step.Name, err))
			return
		}

		// Update step with task run ID.
		persistErr(runID, e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
			ID:        stepResult.ID,
			Status:    "running",
			TaskRunID: sql.NullString{String: result.ID, Valid: result.ID != ""},
			StartedAt: stepNow,
		}))

		// Poll for push completion.
		stepStatus := e.pollPushCompletion(ctx, runID, result.ID)

		stepFinished := sql.NullTime{Time: time.Now(), Valid: true}
		persistErr(runID, e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
			ID:         stepResult.ID,
			Status:     stepStatus,
			TaskRunID:  sql.NullString{String: result.ID, Valid: result.ID != ""},
			StartedAt:  stepNow,
			FinishedAt: stepFinished,
		}))

		if stepStatus == "complete" {
			if step.OnSuccess != "" {
				if idx, ok := stepIndex[step.OnSuccess]; ok {
					currentIdx = idx
					continue
				}
			}
			currentIdx++
		} else {
			// Step failed/aborted.
			if step.OnFailure != "" {
				if idx, ok := stepIndex[step.OnFailure]; ok {
					currentIdx = idx
					continue
				}
			}
			e.failWorkflow(ctx, runID, currentIdx, fmt.Sprintf("step %s: %s", step.Name, stepStatus))
			return
		}
	}

	// All steps completed successfully.
	finished := sql.NullTime{Time: time.Now(), Valid: true}
	persistErr(runID, e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		ID:          runID,
		Status:      "complete",
		CurrentStep: int64(len(def.Steps)),
		StartedAt:   now,
		FinishedAt:  finished,
	}))
	if e.metrics != nil {
		e.metrics.WorkflowRunsTotal.WithLabelValues("complete").Inc()
	}
	slog.Info("workflow: completed", "workflow_run_id", runID)
}

// pollPushCompletion polls a task run until it finishes or the workflow is aborted.
func (e *Engine) pollPushCompletion(ctx context.Context, workflowRunID, taskRunID string) string {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "failed"
		case <-ticker.C:
			if e.isAborted(workflowRunID) {
				return "aborted"
			}

			taskRun, err := e.queries.GetTaskRun(ctx, taskRunID)
			if err != nil {
				slog.Error("workflow: failed to poll task run", "task_run_id", taskRunID, "error", err)
				continue
			}

			switch taskRun.Status {
			case "complete":
				return "complete"
			case "failed":
				return "failed"
			case "running", "pending":
				continue
			default:
				return taskRun.Status
			}
		}
	}
}

// persistErr logs a failed workflow status / step-result write. These
// writes are best-effort progress tracking — the workflow's authoritative
// state is the running goroutine, so a failed write must not abort it,
// but it should not be silent either.
func persistErr(runID string, err error) {
	if err != nil {
		slog.Warn("workflow: failed to persist progress", "workflow_run_id", runID, "error", err)
	}
}

// failWorkflow marks a workflow run as failed.
func (e *Engine) failWorkflow(ctx context.Context, runID string, step int, reason string) {
	finished := sql.NullTime{Time: time.Now(), Valid: true}
	persistErr(runID, e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		ID:            runID,
		Status:        "failed",
		CurrentStep:   int64(step),
		FinishedAt:    finished,
		FailureReason: sql.NullString{String: reason, Valid: true},
	}))
	if e.metrics != nil {
		e.metrics.WorkflowRunsTotal.WithLabelValues("failed").Inc()
	}
	slog.Info("workflow: failed", "workflow_run_id", runID, "reason", reason)
}

// RecoverInterrupted marks any running/pending workflows as failed on server restart.
func RecoverInterrupted(ctx context.Context, queries *db.Queries) error {
	return queries.FailInterruptedWorkflows(ctx)
}
