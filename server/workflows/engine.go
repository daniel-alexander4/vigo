// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

	"github.com/hobnob/hobnob/server/db"
	isgrpc "github.com/hobnob/hobnob/server/grpc"
	"github.com/hobnob/hobnob/server/metrics"
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

// WorkflowDef is a reusable workflow definition loaded from a YAML file.
type WorkflowDef struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Steps       []StepDef `yaml:"steps"`
}

// TaskRunner dispatches tasks and returns results.
type TaskRunner interface {
	DispatchTask(ctx context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error)
}

// Engine executes workflows.
type Engine struct {
	queries    *db.Queries
	runner     TaskRunner
	workflows  map[string]*WorkflowDef
	mu         sync.RWMutex
	abortFlags sync.Map // workflowRunID → struct{}
}

// NewEngine creates a workflow engine.
func NewEngine(queries *db.Queries, runner TaskRunner) *Engine {
	return &Engine{
		queries:   queries,
		runner:    runner,
		workflows: make(map[string]*WorkflowDef),
	}
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
		if ext != ".yaml" && ext != ".yml" {
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

	go e.executeWorkflow(run.ID, def)
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
func (e *Engine) executeWorkflow(runID string, def *WorkflowDef) {
	ctx := context.Background()
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

		// Check abort flag before dispatching.
		if e.isAborted(runID) {
			slog.Info("workflow: aborted", "workflow_run_id", runID, "at_step", step.Name)
			now = sql.NullTime{Time: time.Now(), Valid: true}
			e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
				ID:            runID,
				Status:        "aborted",
				CurrentStep:   int64(currentIdx),
				FinishedAt:    now,
				FailureReason: sql.NullString{String: "operator_abort", Valid: true},
			})
			return
		}

		// Update current step.
		e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
			ID:          runID,
			Status:      "running",
			CurrentStep: int64(currentIdx),
			StartedAt:   now,
		})

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
		e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
			ID:        stepResult.ID,
			Status:    "running",
			StartedAt: stepNow,
		})

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
		result, err := e.runner.DispatchTask(ctx, params)
		if err != nil {
			stepFinished := sql.NullTime{Time: time.Now(), Valid: true}
			e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
				ID:         stepResult.ID,
				Status:     "failed",
				FinishedAt: stepFinished,
			})
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
		e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
			ID:        stepResult.ID,
			Status:    "running",
			TaskRunID: sql.NullString{String: result.ID, Valid: result.ID != ""},
			StartedAt: stepNow,
		})

		// Poll for task completion.
		stepStatus := e.pollTaskCompletion(ctx, runID, result.ID)

		stepFinished := sql.NullTime{Time: time.Now(), Valid: true}
		e.queries.UpdateWorkflowStepResult(ctx, db.UpdateWorkflowStepResultParams{
			ID:         stepResult.ID,
			Status:     stepStatus,
			TaskRunID:  sql.NullString{String: result.ID, Valid: result.ID != ""},
			StartedAt:  stepNow,
			FinishedAt: stepFinished,
		})

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
	e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		ID:          runID,
		Status:      "complete",
		CurrentStep: int64(len(def.Steps)),
		StartedAt:   now,
		FinishedAt:  finished,
	})
	metrics.WorkflowRunsTotal.WithLabelValues("complete").Inc()
	slog.Info("workflow: completed", "workflow_run_id", runID)
}

// pollTaskCompletion polls a task run until it finishes or the workflow is aborted.
func (e *Engine) pollTaskCompletion(ctx context.Context, workflowRunID, taskRunID string) string {
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

			task, err := e.queries.GetTaskRun(ctx, taskRunID)
			if err != nil {
				slog.Error("workflow: failed to poll task", "task_run_id", taskRunID, "error", err)
				continue
			}

			switch task.Status {
			case "complete":
				return "complete"
			case "failed":
				return "failed"
			case "running", "pending":
				continue
			default:
				return task.Status
			}
		}
	}
}

// failWorkflow marks a workflow run as failed.
func (e *Engine) failWorkflow(ctx context.Context, runID string, step int, reason string) {
	finished := sql.NullTime{Time: time.Now(), Valid: true}
	e.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		ID:            runID,
		Status:        "failed",
		CurrentStep:   int64(step),
		FinishedAt:    finished,
		FailureReason: sql.NullString{String: reason, Valid: true},
	})
	metrics.WorkflowRunsTotal.WithLabelValues("failed").Inc()
	slog.Info("workflow: failed", "workflow_run_id", runID, "reason", reason)
}

// RecoverInterrupted marks any running/pending workflows as failed on server restart.
func RecoverInterrupted(ctx context.Context, queries *db.Queries) error {
	return queries.FailInterruptedWorkflows(ctx)
}
