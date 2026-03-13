// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/tasks"
)

// taskRouter tracks in-flight task dispatches and routes incoming
// TaskAck/TaskResult messages from agents to the correct DB records.
type taskRouter struct {
	mu      sync.Mutex
	queries *db.Queries

	// taskRunID → thrallID → task_run_target.ID
	pending map[string]map[string]string

	// taskRunID → thrallID → done channel (closed on completion)
	notifiers map[string]map[string]chan struct{}
}

func newTaskRouter(queries *db.Queries) *taskRouter {
	return &taskRouter{
		queries:   queries,
		pending:   make(map[string]map[string]string),
		notifiers: make(map[string]map[string]chan struct{}),
	}
}

// trackTarget registers a task_run_target so incoming results can be routed.
func (r *taskRouter) trackTarget(taskRunID, thrallID, targetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[taskRunID]
	if !ok {
		targets = make(map[string]string)
		r.pending[taskRunID] = targets
	}
	targets[thrallID] = targetID
}

// lookupTarget returns the task_run_target ID for a given task+thrall.
func (r *taskRouter) lookupTarget(taskRunID, thrallID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[taskRunID]
	if !ok {
		return "", false
	}
	targetID, ok := targets[thrallID]
	return targetID, ok
}

// removeTarget removes a completed target from tracking.
func (r *taskRouter) removeTarget(taskRunID, thrallID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[taskRunID]
	if !ok {
		return
	}
	delete(targets, thrallID)
	if len(targets) == 0 {
		delete(r.pending, taskRunID)
	}
}

// registerNotifier creates a channel that will be closed when the target
// completes. Used by rolling execution to wait for batch completion.
func (r *taskRouter) registerNotifier(taskRunID, thrallID string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	taskNotifiers, ok := r.notifiers[taskRunID]
	if !ok {
		taskNotifiers = make(map[string]chan struct{})
		r.notifiers[taskRunID] = taskNotifiers
	}
	ch := make(chan struct{})
	taskNotifiers[thrallID] = ch
	return ch
}

// removeNotifier removes a notifier channel for a target.
func (r *taskRouter) removeNotifier(taskRunID, thrallID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	taskNotifiers, ok := r.notifiers[taskRunID]
	if !ok {
		return
	}
	delete(taskNotifiers, thrallID)
	if len(taskNotifiers) == 0 {
		delete(r.notifiers, taskRunID)
	}
}

// notifyCompletion signals that a target has completed (result received or
// rejected). Closes the notifier channel if one is registered.
func (r *taskRouter) notifyCompletion(taskRunID, thrallID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	taskNotifiers, ok := r.notifiers[taskRunID]
	if !ok {
		return
	}
	if ch, ok := taskNotifiers[thrallID]; ok {
		close(ch)
		delete(taskNotifiers, thrallID)
	}
}

// handleTaskAck processes an incoming TaskAck from an agent.
func (r *taskRouter) handleTaskAck(ctx context.Context, thrallID string, ack *pb.TaskAck) {
	targetID, ok := r.lookupTarget(ack.TaskId, thrallID)
	if !ok {
		slog.Warn("received task ack for unknown target",
			"task_id", ack.TaskId, "thrall_id", thrallID)
		return
	}

	if !ack.Accepted {
		// Agent rejected the task.
		now := sql.NullTime{Time: time.Now(), Valid: true}
		if err := r.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
			ID:         targetID,
			Status:     "rejected",
			ErrorMsg:   sql.NullString{String: ack.RejectReason, Valid: ack.RejectReason != ""},
			FinishedAt: now,
		}); err != nil {
			slog.Error("failed to update rejected task target",
				"target_id", targetID, "error", err)
		}
		r.removeTarget(ack.TaskId, thrallID)
		r.notifyCompletion(ack.TaskId, thrallID)
		r.maybeFinishTaskRun(ctx, ack.TaskId)
		return
	}

	// Agent accepted — update dispatched_at timestamp.
	now := sql.NullTime{Time: time.Now(), Valid: true}
	if err := r.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
		ID:           targetID,
		Status:       "dispatched",
		DispatchedAt: now,
	}); err != nil {
		slog.Error("failed to update dispatched task target",
			"target_id", targetID, "error", err)
	}
}

// handleTaskResult processes an incoming TaskResult from an agent.
func (r *taskRouter) handleTaskResult(ctx context.Context, thrallID string, result *pb.TaskResult) {
	targetID, ok := r.lookupTarget(result.TaskId, thrallID)
	if !ok {
		slog.Warn("received task result for unknown target",
			"task_id", result.TaskId, "thrall_id", thrallID)
		return
	}

	now := sql.NullTime{Time: time.Now(), Valid: true}
	if err := r.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
		ID:         targetID,
		Status:     result.Status,
		ExitCode:   sql.NullInt64{Int64: int64(result.ExitCode), Valid: true},
		Stdout:     sql.NullString{String: string(result.Stdout), Valid: len(result.Stdout) > 0},
		Stderr:     sql.NullString{String: string(result.Stderr), Valid: len(result.Stderr) > 0},
		DurationMs: sql.NullInt64{Int64: int64(result.DurationMs), Valid: true},
		ErrorMsg:   sql.NullString{String: result.ErrorMessage, Valid: result.ErrorMessage != ""},
		FinishedAt: now,
	}); err != nil {
		slog.Error("failed to update task result",
			"target_id", targetID, "error", err)
	}

	slog.Info("task result received",
		"task_id", result.TaskId,
		"thrall_id", thrallID,
		"status", result.Status,
		"exit_code", result.ExitCode,
		"duration_ms", result.DurationMs,
	)

	r.removeTarget(result.TaskId, thrallID)
	r.notifyCompletion(result.TaskId, thrallID)
	r.maybeFinishTaskRun(ctx, result.TaskId)
}

// maybeFinishTaskRun checks if all targets for a task run are done and
// updates the task_run status accordingly.
func (r *taskRouter) maybeFinishTaskRun(ctx context.Context, taskRunID string) {
	counts, err := r.queries.CountTaskRunTargetsByStatus(ctx, taskRunID)
	if err != nil {
		slog.Error("failed to count task run targets", "task_run_id", taskRunID, "error", err)
		return
	}

	var totalDone, totalFailed int64
	var total int64
	for _, c := range counts {
		total += c.Count
		switch c.Status {
		case "pending", "dispatched":
			// Still in progress.
			return
		case "failed", "timeout", "rejected":
			totalDone += c.Count
			totalFailed += c.Count
		default:
			totalDone += c.Count
		}
	}

	// All targets are done.
	status := "complete"
	if totalFailed > 0 {
		status = "failed"
	}

	now := sql.NullTime{Time: time.Now(), Valid: true}
	if err := r.queries.UpdateTaskRunStatus(ctx, db.UpdateTaskRunStatusParams{
		ID:         taskRunID,
		Status:     status,
		FinishedAt: now,
	}); err != nil {
		slog.Error("failed to finalize task run", "task_run_id", taskRunID, "error", err)
		return
	}

	slog.Info("task run finished",
		"task_run_id", taskRunID,
		"status", status,
		"total", total,
		"failed", totalFailed,
	)
}

// DispatchTask creates a task run, resolves targets, dispatches to connected
// agents, and marks offline agents. Supports rolling execution with batch_size,
// max_failures, and health_check parameters. If TaskName is set, the named
// task definition is resolved from the task store, its parameters are validated,
// and the script+env are injected into the dispatch.
func (s *AgentService) DispatchTask(ctx context.Context, params DispatchParams) (*DispatchResult, error) {
	// Resolve named task if specified.
	if params.TaskName != "" {
		if s.taskStore == nil {
			return nil, fmt.Errorf("task store not configured")
		}
		def, ok := s.taskStore.Get(params.TaskName)
		if !ok {
			return nil, fmt.Errorf("unknown task: %s", params.TaskName)
		}
		if err := tasks.ValidateParams(def, params.Params); err != nil {
			return nil, fmt.Errorf("task %s: %s", params.TaskName, err)
		}
		params.Command = def.Script
		if params.TimeoutSecs <= 0 {
			params.TimeoutSecs = def.Timeout
		}
		if params.TargetSpec == "" && def.Target != "" {
			params.TargetSpec = def.Target
		}
	}

	// Build env vars for named tasks.
	var taskEnv map[string]string
	if params.TaskName != "" {
		if def, ok := s.taskStore.Get(params.TaskName); ok {
			taskEnv = tasks.BuildEnv(def, params.Params)
		}
	}

	// Resolve targets.
	targets, err := resolveTargets(ctx, s.queries, params.TargetSpec, s.fleetIndex)
	if err != nil {
		return nil, err
	}

	timeoutSecs := params.TimeoutSecs
	if timeoutSecs <= 0 {
		timeoutSecs = 60
	}

	// Separate connected vs offline targets.
	connected := s.streamMgr.connectedThrallIDs()
	var onlineTargets, offlineTargets []targetInfo
	for _, t := range targets {
		if connected[t.ThrallID] {
			onlineTargets = append(onlineTargets, t)
		} else {
			offlineTargets = append(offlineTargets, t)
		}
	}

	// Parse batch size.
	batchSize, err := parseBatchSpec(params.BatchSize, len(onlineTargets))
	if err != nil {
		return nil, err
	}

	// Plan batches.
	batches := planBatches(onlineTargets, batchSize)

	// Dry-run: return batch plan without executing.
	if params.DryRun {
		result := &DispatchResult{Status: "dry_run"}
		for i, batch := range batches {
			hostnames := make([]string, 0, len(batch))
			for _, t := range batch {
				hostnames = append(hostnames, t.Hostname)
			}
			result.Batches = append(result.Batches, DryRunBatch{Index: i + 1, Hostnames: hostnames})
		}
		for _, t := range offlineTargets {
			result.Offline = append(result.Offline, t.Hostname)
		}
		return result, nil
	}

	// Create the task run record.
	taskRun, err := s.queries.CreateTaskRun(ctx, db.CreateTaskRunParams{
		Command:      params.Command,
		TargetGlob:   params.TargetSpec,
		Operator:     params.Operator,
		TotalTargets: int64(len(targets)),
		TimeoutSecs:  int64(timeoutSecs),
		BatchSize:    sql.NullString{String: params.BatchSize, Valid: params.BatchSize != ""},
		MaxFailures:  sql.NullString{String: params.MaxFailures, Valid: params.MaxFailures != ""},
	})
	if err != nil {
		return nil, err
	}

	now := sql.NullTime{Time: time.Now(), Valid: true}

	// Create offline target records.
	for _, t := range offlineTargets {
		if _, err := s.queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{
			TaskRunID: taskRun.ID,
			ThrallID:  t.ThrallID,
			Hostname:  t.Hostname,
			Status:    "offline",
		}); err != nil {
			slog.Error("failed to create offline task target",
				"task_run_id", taskRun.ID, "thrall_id", t.ThrallID, "error", err)
		}
	}

	rolling := batchSize > 0 && len(batches) > 1

	if rolling {
		// Rolling execution: create all target records upfront, then dispatch
		// batch-by-batch via a background goroutine.
		batchRecords := make([][]targetRecord, len(batches))
		for i, batch := range batches {
			batchRecords[i] = make([]targetRecord, 0, len(batch))
			for _, t := range batch {
				target, err := s.queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{
					TaskRunID: taskRun.ID,
					ThrallID:  t.ThrallID,
					Hostname:  t.Hostname,
					Status:    "pending",
				})
				if err != nil {
					slog.Error("failed to create task run target",
						"task_run_id", taskRun.ID, "thrall_id", t.ThrallID, "error", err)
					continue
				}
				batchRecords[i] = append(batchRecords[i], targetRecord{info: t, targetID: target.ID})
			}
		}

		if err := s.queries.UpdateTaskRunStatus(ctx, db.UpdateTaskRunStatusParams{
			ID:        taskRun.ID,
			Status:    "running",
			StartedAt: now,
		}); err != nil {
			slog.Error("failed to start task run", "task_run_id", taskRun.ID, "error", err)
		}

		go s.runRollingExecution(taskRun.ID, batchRecords, params.Command, timeoutSecs, params.MaxFailures, params.HealthCheck, taskEnv, len(onlineTargets))
	} else {
		// Non-batched: dispatch all targets inline (original behavior).
		dispatched := 0
		for _, t := range onlineTargets {
			target, err := s.queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{
				TaskRunID: taskRun.ID,
				ThrallID:  t.ThrallID,
				Hostname:  t.Hostname,
				Status:    "pending",
			})
			if err != nil {
				slog.Error("failed to create task run target",
					"task_run_id", taskRun.ID, "thrall_id", t.ThrallID, "error", err)
				continue
			}

			s.taskRouter.trackTarget(taskRun.ID, t.ThrallID, target.ID)

			msg := &pb.ServerStreamMessage{
				Msg: &pb.ServerStreamMessage_TaskDispatch{
					TaskDispatch: &pb.TaskDispatch{
						TaskId:         taskRun.ID,
						Command:        params.Command,
						TimeoutSeconds: timeoutSecs,
						Env:            taskEnv,
					},
				},
			}

			if s.streamMgr.dispatchTask(t.ThrallID, msg) {
				dispatched++
			} else {
				// Stream closed between check and send — mark offline.
				if err := s.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
					ID:         target.ID,
					Status:     "offline",
					FinishedAt: now,
				}); err != nil {
					slog.Error("failed to mark target offline",
						"target_id", target.ID, "error", err)
				}
				s.taskRouter.removeTarget(taskRun.ID, t.ThrallID)
			}
		}

		// Update task run to running.
		if err := s.queries.UpdateTaskRunStatus(ctx, db.UpdateTaskRunStatusParams{
			ID:        taskRun.ID,
			Status:    "running",
			StartedAt: now,
		}); err != nil {
			slog.Error("failed to start task run", "task_run_id", taskRun.ID, "error", err)
		}

		// If no targets were dispatched, finalize immediately.
		if dispatched == 0 {
			s.taskRouter.maybeFinishTaskRun(ctx, taskRun.ID)
		}
	}

	slog.Info("task dispatched",
		"task_run_id", taskRun.ID,
		"command", params.Command,
		"target_spec", params.TargetSpec,
		"total_targets", len(targets),
		"online", len(onlineTargets),
		"offline", len(offlineTargets),
		"rolling", rolling,
	)

	return &DispatchResult{ID: taskRun.ID, Status: "running"}, nil
}
