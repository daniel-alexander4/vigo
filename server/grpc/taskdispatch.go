// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/task"
	"git.alexander4.io/vigo/server/validate"
)

// taskRouter tracks in-flight task dispatches and routes incoming
// TaskAck/TaskResult messages from agents to the correct DB records.
type taskRouter struct {
	mu      sync.Mutex
	queries *db.Queries
	metrics *metrics.Metrics // per-instance prometheus collectors

	// taskRunID → envoyID → task_run_target.ID
	pending map[string]map[string]string

	// taskRunID → envoyID → done channel (closed on completion)
	notifiers map[string]map[string]chan struct{}
}

// taskRunStatusCancelled is the task_runs.status the CancelTaskRun /
// CancelAllRunningTaskRuns queries write (server/db/queries/task_runs.sql).
const taskRunStatusCancelled = "cancelled"

func newTaskRouter(queries *db.Queries, m *metrics.Metrics) *taskRouter {
	return &taskRouter{
		queries:   queries,
		metrics:   m,
		pending:   make(map[string]map[string]string),
		notifiers: make(map[string]map[string]chan struct{}),
	}
}

// trackTarget registers a task_run_target so incoming results can be routed.
func (r *taskRouter) trackTarget(taskRunID, envoyID, targetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[taskRunID]
	if !ok {
		targets = make(map[string]string)
		r.pending[taskRunID] = targets
	}
	targets[envoyID] = targetID
}

// lookupTarget returns the task_run_target ID for a given push+envoy.
func (r *taskRouter) lookupTarget(taskRunID, envoyID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[taskRunID]
	if !ok {
		return "", false
	}
	targetID, ok := targets[envoyID]
	return targetID, ok
}

// removeTarget removes a completed target from tracking.
func (r *taskRouter) removeTarget(taskRunID, envoyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[taskRunID]
	if !ok {
		return
	}
	delete(targets, envoyID)
	if len(targets) == 0 {
		delete(r.pending, taskRunID)
	}
}

// registerNotifier creates a channel that will be closed when the target
// completes. Used by rolling execution to wait for batch completion.
func (r *taskRouter) registerNotifier(taskRunID, envoyID string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	taskNotifiers, ok := r.notifiers[taskRunID]
	if !ok {
		taskNotifiers = make(map[string]chan struct{})
		r.notifiers[taskRunID] = taskNotifiers
	}
	ch := make(chan struct{})
	taskNotifiers[envoyID] = ch
	return ch
}

// removeNotifier removes a notifier channel for a target.
func (r *taskRouter) removeNotifier(taskRunID, envoyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	taskNotifiers, ok := r.notifiers[taskRunID]
	if !ok {
		return
	}
	delete(taskNotifiers, envoyID)
	if len(taskNotifiers) == 0 {
		delete(r.notifiers, taskRunID)
	}
}

// notifyCompletion signals that a target has completed (result received or
// rejected). Closes the notifier channel if one is registered.
func (r *taskRouter) notifyCompletion(taskRunID, envoyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	taskNotifiers, ok := r.notifiers[taskRunID]
	if !ok {
		return
	}
	if ch, ok := taskNotifiers[envoyID]; ok {
		close(ch)
		delete(taskNotifiers, envoyID)
	}
}

// failEnvoyTargets marks every target still tracked for envoyID failed — the
// envoy's held stream went away (dropped, revoked, deleted) before it sent a
// result, and a result can only arrive on the stream the dispatch rode. Without
// this the pending entry and the task_run stayed "running" forever. Each
// affected task run is then finalized if that was its last open target.
func (r *taskRouter) failEnvoyTargets(ctx context.Context, envoyID, reason string) int {
	type orphan struct{ taskRunID, targetID string }
	var orphans []orphan
	r.mu.Lock()
	for taskRunID, targets := range r.pending {
		if targetID, ok := targets[envoyID]; ok {
			orphans = append(orphans, orphan{taskRunID, targetID})
			delete(targets, envoyID)
			if len(targets) == 0 {
				delete(r.pending, taskRunID)
			}
		}
	}
	r.mu.Unlock()

	now := sql.NullTime{Time: time.Now(), Valid: true}
	for _, o := range orphans {
		if r.queries != nil {
			if err := r.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
				ID:         o.targetID,
				Status:     "failed",
				ErrorMsg:   sql.NullString{String: reason, Valid: true},
				FinishedAt: now,
			}); err != nil {
				slog.Error("failed to mark orphaned task target failed",
					"target_id", o.targetID, "error", err)
			}
		}
		r.notifyCompletion(o.taskRunID, envoyID)
		if r.queries != nil {
			r.maybeFinishTaskRun(ctx, o.taskRunID)
		}
		slog.Warn("task target failed: agent stream closed before a result",
			"task_run_id", o.taskRunID, "envoy_id", envoyID, "reason", reason)
	}
	return len(orphans)
}

// handleTaskAck processes an incoming TaskAck from an agent. Returns whether
// the ack matched a tracked target (an unknown or duplicate ack is ignored).
func (r *taskRouter) handleTaskAck(ctx context.Context, envoyID string, ack *pb.TaskAck) bool {
	targetID, ok := r.lookupTarget(ack.TaskId, envoyID)
	if !ok {
		slog.Warn("received task ack for unknown target",
			"task_id", ack.TaskId, "envoy_id", envoyID)
		return false
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
			slog.Error("failed to update rejected push target",
				"target_id", targetID, "error", err)
		}
		r.removeTarget(ack.TaskId, envoyID)
		r.notifyCompletion(ack.TaskId, envoyID)
		r.maybeFinishTaskRun(ctx, ack.TaskId)
		return true
	}

	// Agent accepted — update dispatched_at timestamp.
	now := sql.NullTime{Time: time.Now(), Valid: true}
	if err := r.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
		ID:           targetID,
		Status:       "dispatched",
		DispatchedAt: now,
	}); err != nil {
		slog.Error("failed to update dispatched push target",
			"target_id", targetID, "error", err)
	}
	return true
}

// handleTaskResult processes an incoming TaskResult from an agent. Returns
// whether the result matched a tracked target (an unknown or duplicate result
// is ignored).
func (r *taskRouter) handleTaskResult(ctx context.Context, envoyID string, result *pb.TaskResult) bool {
	targetID, ok := r.lookupTarget(result.TaskId, envoyID)
	if !ok {
		slog.Warn("received task result for unknown target",
			"task_id", result.TaskId, "envoy_id", envoyID)
		return false
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

	_, traceID := ParseOrGenerate(result.GetTraceparent())
	slog.Info("task result received",
		"task_id", result.TaskId,
		"envoy_id", envoyID,
		"trace_id", traceID,
		"status", result.Status,
		"exit_code", result.ExitCode,
		"duration_ms", result.DurationMs,
	)

	r.removeTarget(result.TaskId, envoyID)
	r.notifyCompletion(result.TaskId, envoyID)
	r.maybeFinishTaskRun(ctx, result.TaskId)
	return true
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

	// All targets are done. A cancelled run keeps its status: the cancel
	// already set finished_at, and a late result from a target dispatched
	// before the cancel must not rewrite it to complete/failed.
	if run, err := r.queries.GetTaskRun(ctx, taskRunID); err == nil && run.Status == taskRunStatusCancelled {
		return
	}
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

	r.metrics.TaskRunsTotal.WithLabelValues(status).Inc()

	slog.Info("task run finished",
		"task_run_id", taskRunID,
		"status", status,
		"total", total,
		"failed", totalFailed,
	)
}

// DispatchPush creates a task run, resolves targets, dispatches to connected
// agents, and marks offline agents. Supports rolling execution with batch_size,
// max_failures, and health_check parameters. If TaskName is set, the named
// task definition is resolved from the task store, its parameters are validated,
// and the script+env are injected into the dispatch.
func (s *AgentService) DispatchPush(ctx context.Context, params DispatchParams) (*DispatchResult, error) {
	// Resolve named task definition if specified.
	if params.TaskName != "" {
		if s.taskStore == nil {
			return nil, fmt.Errorf("task store not configured")
		}
		def, ok := s.taskStore.Get(params.TaskName)
		if !ok {
			return nil, fmt.Errorf("unknown task definition: %s", params.TaskName)
		}
		if err := task.ValidateParams(def, params.Params); err != nil {
			return nil, fmt.Errorf("task %s: %s", params.TaskName, err)
		}
		params.Command = def.Script
		if params.TimeoutSecs <= 0 {
			params.TimeoutSecs = def.Timeout
		}
		if params.Nice <= 0 && def.Nice > 0 {
			params.Nice = def.Nice
		}
		if params.TargetSpec == "" && def.Target != "" {
			params.TargetSpec = def.Target
		}
	}

	// Self-protection guardrails — reject commands that target vigo
	// infrastructure. Force skips this server-side gate; the agent's
	// hardcoded guardrails still enforce. The returned *validate.Violation
	// is unwrapped so REST callers errors.As it into a structured 400.
	if !params.Force {
		if err := validate.ValidatePushCommand(params.Command); err != nil {
			return nil, err
		}
	}

	// Build env vars for named task definitions.
	var taskEnv map[string]string
	if params.TaskName != "" {
		if def, ok := s.taskStore.Get(params.TaskName); ok {
			taskEnv = task.BuildEnv(def, params.Params)
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

	// Separate connected vs promotable vs offline targets.
	connected := s.streamMgr.connectedEnvoyIDs()
	var onlineTargets, promotableTargets, offlineTargets []targetInfo
	for _, t := range targets {
		if connected[t.EnvoyID] {
			onlineTargets = append(onlineTargets, t)
		} else if s.isRecentlyActive(t.EnvoyID) {
			promotableTargets = append(promotableTargets, t)
		} else {
			offlineTargets = append(offlineTargets, t)
		}
	}

	// Parse batch size (promotable targets count as dispatchable).
	dispatchable := append(onlineTargets, promotableTargets...)
	batchSize, err := parseBatchSpec(params.BatchSize, len(dispatchable))
	if err != nil {
		return nil, err
	}

	// Plan batches.
	batches := planBatches(dispatchable, batchSize)

	// Dry-run: return batch plan without executing.
	if params.DryRun {
		result := &DispatchResult{Status: StatusDryRun}
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

	// One trace_id per task run — shared across every TaskDispatch target
	// and echoed back in each TaskResult (ADR-028).
	traceparent, _ := GenerateTraceparent()

	now := sql.NullTime{Time: time.Now(), Valid: true}

	// Create offline target records.
	for _, t := range offlineTargets {
		if _, err := s.queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{
			TaskRunID: taskRun.ID,
			EnvoyID:   t.EnvoyID,
			Hostname:  t.Hostname,
			Status:    "offline",
		}); err != nil {
			slog.Error("failed to create offline push target",
				"task_run_id", taskRun.ID, "envoy_id", t.EnvoyID, "error", err)
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
					EnvoyID:   t.EnvoyID,
					Hostname:  t.Hostname,
					Status:    "pending",
				})
				if err != nil {
					slog.Error("failed to create task run target",
						"task_run_id", taskRun.ID, "envoy_id", t.EnvoyID, "error", err)
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

		go s.runRollingExecution(taskRun.ID, batchRecords, params.Command, timeoutSecs, params.Nice, params.MaxFailures, params.HealthCheck, taskEnv, len(dispatchable), traceparent)
	} else {
		// Non-batched: dispatch all targets inline (original behavior).
		dispatched := 0
		for _, t := range onlineTargets {
			target, err := s.queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{
				TaskRunID: taskRun.ID,
				EnvoyID:   t.EnvoyID,
				Hostname:  t.Hostname,
				Status:    "pending",
			})
			if err != nil {
				slog.Error("failed to create task run target",
					"task_run_id", taskRun.ID, "envoy_id", t.EnvoyID, "error", err)
				continue
			}

			s.taskRouter.trackTarget(taskRun.ID, t.EnvoyID, target.ID)

			msg := &pb.ServerStreamMessage{
				Msg: &pb.ServerStreamMessage_TaskDispatch{
					TaskDispatch: &pb.TaskDispatch{
						TaskId:         taskRun.ID,
						Command:        params.Command,
						TimeoutSeconds: timeoutSecs,
						Env:            taskEnv,
						Nice:           params.Nice,
						Traceparent:    traceparent,
					},
				},
			}

			if s.streamMgr.dispatchPush(t.EnvoyID, msg) {
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
				s.taskRouter.removeTarget(taskRun.ID, t.EnvoyID)
			}
		}

		// Queue promotable targets for stream promotion.
		for _, t := range promotableTargets {
			target, err := s.queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{
				TaskRunID: taskRun.ID,
				EnvoyID:   t.EnvoyID,
				Hostname:  t.Hostname,
				Status:    "pending",
			})
			if err != nil {
				slog.Error("failed to create promotable task run target",
					"task_run_id", taskRun.ID, "envoy_id", t.EnvoyID, "error", err)
				continue
			}

			s.taskRouter.trackTarget(taskRun.ID, t.EnvoyID, target.ID)
			dispatched++

			msg := &pb.ServerStreamMessage{
				Msg: &pb.ServerStreamMessage_TaskDispatch{
					TaskDispatch: &pb.TaskDispatch{
						TaskId:         taskRun.ID,
						Command:        params.Command,
						TimeoutSeconds: timeoutSecs,
						Env:            taskEnv,
						Nice:           params.Nice,
						Traceparent:    traceparent,
					},
				},
			}

			targetID := target.ID
			envoyID := t.EnvoyID
			taskRunID := taskRun.ID
			// Wake the agent so it checks in immediately and opens a stream.
			if s.fleetIndex != nil {
				s.fleetIndex.SignalWake(t.EnvoyID)
			}

			s.streamMgr.queueForPromotion(t.EnvoyID, msg, func() {
				if err := s.queries.UpdateTaskRunTarget(context.Background(), db.UpdateTaskRunTargetParams{
					ID:         targetID,
					Status:     "offline",
					FinishedAt: sql.NullTime{Time: time.Now(), Valid: true},
				}); err != nil {
					slog.Error("failed to mark promoted target offline",
						"target_id", targetID, "error", err)
				}
				s.taskRouter.removeTarget(taskRunID, envoyID)
				s.taskRouter.maybeFinishTaskRun(context.Background(), taskRunID)
			})
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

	slog.Info("push dispatched",
		"task_run_id", taskRun.ID,
		"command", params.Command,
		"target_spec", params.TargetSpec,
		"total_targets", len(targets),
		"online", len(onlineTargets),
		"promotable", len(promotableTargets),
		"offline", len(offlineTargets),
		"rolling", rolling,
	)

	return &DispatchResult{
		ID:         taskRun.ID,
		Status:     "running",
		Dispatched: len(onlineTargets) + len(promotableTargets),
	}, nil
}
