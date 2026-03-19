// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/metrics"
	"github.com/daniel-alexander4/vigo/server/push"
	"github.com/daniel-alexander4/vigo/server/validate"
)

// pushRouter tracks in-flight push dispatches and routes incoming
// PushAck/PushResult messages from agents to the correct DB records.
type pushRouter struct {
	mu      sync.Mutex
	queries *db.Queries

	// pushRunID → envoyID → push_run_target.ID
	pending map[string]map[string]string

	// pushRunID → envoyID → done channel (closed on completion)
	notifiers map[string]map[string]chan struct{}
}

func newPushRouter(queries *db.Queries) *pushRouter {
	return &pushRouter{
		queries:   queries,
		pending:   make(map[string]map[string]string),
		notifiers: make(map[string]map[string]chan struct{}),
	}
}

// trackTarget registers a push_run_target so incoming results can be routed.
func (r *pushRouter) trackTarget(pushRunID, envoyID, targetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[pushRunID]
	if !ok {
		targets = make(map[string]string)
		r.pending[pushRunID] = targets
	}
	targets[envoyID] = targetID
}

// lookupTarget returns the push_run_target ID for a given push+envoy.
func (r *pushRouter) lookupTarget(pushRunID, envoyID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[pushRunID]
	if !ok {
		return "", false
	}
	targetID, ok := targets[envoyID]
	return targetID, ok
}

// removeTarget removes a completed target from tracking.
func (r *pushRouter) removeTarget(pushRunID, envoyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	targets, ok := r.pending[pushRunID]
	if !ok {
		return
	}
	delete(targets, envoyID)
	if len(targets) == 0 {
		delete(r.pending, pushRunID)
	}
}

// registerNotifier creates a channel that will be closed when the target
// completes. Used by rolling execution to wait for batch completion.
func (r *pushRouter) registerNotifier(pushRunID, envoyID string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	pushNotifiers, ok := r.notifiers[pushRunID]
	if !ok {
		pushNotifiers = make(map[string]chan struct{})
		r.notifiers[pushRunID] = pushNotifiers
	}
	ch := make(chan struct{})
	pushNotifiers[envoyID] = ch
	return ch
}

// removeNotifier removes a notifier channel for a target.
func (r *pushRouter) removeNotifier(pushRunID, envoyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pushNotifiers, ok := r.notifiers[pushRunID]
	if !ok {
		return
	}
	delete(pushNotifiers, envoyID)
	if len(pushNotifiers) == 0 {
		delete(r.notifiers, pushRunID)
	}
}

// notifyCompletion signals that a target has completed (result received or
// rejected). Closes the notifier channel if one is registered.
func (r *pushRouter) notifyCompletion(pushRunID, envoyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pushNotifiers, ok := r.notifiers[pushRunID]
	if !ok {
		return
	}
	if ch, ok := pushNotifiers[envoyID]; ok {
		close(ch)
		delete(pushNotifiers, envoyID)
	}
}

// handlePushAck processes an incoming PushAck from an agent.
func (r *pushRouter) handlePushAck(ctx context.Context, envoyID string, ack *pb.PushAck) {
	targetID, ok := r.lookupTarget(ack.PushId, envoyID)
	if !ok {
		slog.Warn("received push ack for unknown target",
			"push_id", ack.PushId, "envoy_id", envoyID)
		return
	}

	if !ack.Accepted {
		// Agent rejected the push.
		now := sql.NullTime{Time: time.Now(), Valid: true}
		if err := r.queries.UpdatePushRunTarget(ctx, db.UpdatePushRunTargetParams{
			ID:         targetID,
			Status:     "rejected",
			ErrorMsg:   sql.NullString{String: ack.RejectReason, Valid: ack.RejectReason != ""},
			FinishedAt: now,
		}); err != nil {
			slog.Error("failed to update rejected push target",
				"target_id", targetID, "error", err)
		}
		r.removeTarget(ack.PushId, envoyID)
		r.notifyCompletion(ack.PushId, envoyID)
		r.maybeFinishPushRun(ctx, ack.PushId)
		return
	}

	// Agent accepted — update dispatched_at timestamp.
	now := sql.NullTime{Time: time.Now(), Valid: true}
	if err := r.queries.UpdatePushRunTarget(ctx, db.UpdatePushRunTargetParams{
		ID:           targetID,
		Status:       "dispatched",
		DispatchedAt: now,
	}); err != nil {
		slog.Error("failed to update dispatched push target",
			"target_id", targetID, "error", err)
	}
}

// handlePushResult processes an incoming PushResult from an agent.
func (r *pushRouter) handlePushResult(ctx context.Context, envoyID string, result *pb.PushResult) {
	targetID, ok := r.lookupTarget(result.PushId, envoyID)
	if !ok {
		slog.Warn("received push result for unknown target",
			"push_id", result.PushId, "envoy_id", envoyID)
		return
	}

	now := sql.NullTime{Time: time.Now(), Valid: true}
	if err := r.queries.UpdatePushRunTarget(ctx, db.UpdatePushRunTargetParams{
		ID:         targetID,
		Status:     result.Status,
		ExitCode:   sql.NullInt64{Int64: int64(result.ExitCode), Valid: true},
		Stdout:     sql.NullString{String: string(result.Stdout), Valid: len(result.Stdout) > 0},
		Stderr:     sql.NullString{String: string(result.Stderr), Valid: len(result.Stderr) > 0},
		DurationMs: sql.NullInt64{Int64: int64(result.DurationMs), Valid: true},
		ErrorMsg:   sql.NullString{String: result.ErrorMessage, Valid: result.ErrorMessage != ""},
		FinishedAt: now,
	}); err != nil {
		slog.Error("failed to update push result",
			"target_id", targetID, "error", err)
	}

	slog.Info("push result received",
		"push_id", result.PushId,
		"envoy_id", envoyID,
		"status", result.Status,
		"exit_code", result.ExitCode,
		"duration_ms", result.DurationMs,
	)

	r.removeTarget(result.PushId, envoyID)
	r.notifyCompletion(result.PushId, envoyID)
	r.maybeFinishPushRun(ctx, result.PushId)
}

// maybeFinishPushRun checks if all targets for a push run are done and
// updates the push_run status accordingly.
func (r *pushRouter) maybeFinishPushRun(ctx context.Context, pushRunID string) {
	counts, err := r.queries.CountPushRunTargetsByStatus(ctx, pushRunID)
	if err != nil {
		slog.Error("failed to count push run targets", "push_run_id", pushRunID, "error", err)
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
	if err := r.queries.UpdatePushRunStatus(ctx, db.UpdatePushRunStatusParams{
		ID:         pushRunID,
		Status:     status,
		FinishedAt: now,
	}); err != nil {
		slog.Error("failed to finalize push run", "push_run_id", pushRunID, "error", err)
		return
	}

	metrics.PushRunsTotal.WithLabelValues(status).Inc()

	slog.Info("push run finished",
		"push_run_id", pushRunID,
		"status", status,
		"total", total,
		"failed", totalFailed,
	)
}

// DispatchPush creates a push run, resolves targets, dispatches to connected
// agents, and marks offline agents. Supports rolling execution with batch_size,
// max_failures, and health_check parameters. If TaskName is set, the named
// push definition is resolved from the push store, its parameters are validated,
// and the script+env are injected into the dispatch.
func (s *AgentService) DispatchPush(ctx context.Context, params DispatchParams) (*DispatchResult, error) {
	// Resolve named push definition if specified.
	if params.TaskName != "" {
		if s.pushStore == nil {
			return nil, fmt.Errorf("push store not configured")
		}
		def, ok := s.pushStore.Get(params.TaskName)
		if !ok {
			return nil, fmt.Errorf("unknown push definition: %s", params.TaskName)
		}
		if err := push.ValidateParams(def, params.Params); err != nil {
			return nil, fmt.Errorf("push %s: %s", params.TaskName, err)
		}
		params.Command = def.Script
		if params.TimeoutSecs <= 0 {
			params.TimeoutSecs = def.Timeout
		}
		if params.TargetSpec == "" && def.Target != "" {
			params.TargetSpec = def.Target
		}
	}

	// Self-protection guardrails — reject commands that target vigo infrastructure.
	if err := validate.ValidatePushCommand(params.Command); err != nil {
		return nil, fmt.Errorf("guardrail violation: %s", err)
	}

	// Build env vars for named push definitions.
	var pushEnv map[string]string
	if params.TaskName != "" {
		if def, ok := s.pushStore.Get(params.TaskName); ok {
			pushEnv = push.BuildEnv(def, params.Params)
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

	// Create the push run record.
	pushRun, err := s.queries.CreatePushRun(ctx, db.CreatePushRunParams{
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
		if _, err := s.queries.CreatePushRunTarget(ctx, db.CreatePushRunTargetParams{
			PushRunID: pushRun.ID,
			EnvoyID:  t.EnvoyID,
			Hostname:  t.Hostname,
			Status:    "offline",
		}); err != nil {
			slog.Error("failed to create offline push target",
				"push_run_id", pushRun.ID, "envoy_id", t.EnvoyID, "error", err)
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
				target, err := s.queries.CreatePushRunTarget(ctx, db.CreatePushRunTargetParams{
					PushRunID: pushRun.ID,
					EnvoyID:  t.EnvoyID,
					Hostname:  t.Hostname,
					Status:    "pending",
				})
				if err != nil {
					slog.Error("failed to create push run target",
						"push_run_id", pushRun.ID, "envoy_id", t.EnvoyID, "error", err)
					continue
				}
				batchRecords[i] = append(batchRecords[i], targetRecord{info: t, targetID: target.ID})
			}
		}

		if err := s.queries.UpdatePushRunStatus(ctx, db.UpdatePushRunStatusParams{
			ID:        pushRun.ID,
			Status:    "running",
			StartedAt: now,
		}); err != nil {
			slog.Error("failed to start push run", "push_run_id", pushRun.ID, "error", err)
		}

		go s.runRollingExecution(pushRun.ID, batchRecords, params.Command, timeoutSecs, params.MaxFailures, params.HealthCheck, pushEnv, len(dispatchable))
	} else {
		// Non-batched: dispatch all targets inline (original behavior).
		dispatched := 0
		for _, t := range onlineTargets {
			target, err := s.queries.CreatePushRunTarget(ctx, db.CreatePushRunTargetParams{
				PushRunID: pushRun.ID,
				EnvoyID:  t.EnvoyID,
				Hostname:  t.Hostname,
				Status:    "pending",
			})
			if err != nil {
				slog.Error("failed to create push run target",
					"push_run_id", pushRun.ID, "envoy_id", t.EnvoyID, "error", err)
				continue
			}

			s.pushRouter.trackTarget(pushRun.ID, t.EnvoyID, target.ID)

			msg := &pb.ServerStreamMessage{
				Msg: &pb.ServerStreamMessage_PushDispatch{
					PushDispatch: &pb.PushDispatch{
						PushId:         pushRun.ID,
						Command:        params.Command,
						TimeoutSeconds: timeoutSecs,
						Env:            pushEnv,
					},
				},
			}

			if s.streamMgr.dispatchPush(t.EnvoyID, msg) {
				dispatched++
			} else {
				// Stream closed between check and send — mark offline.
				if err := s.queries.UpdatePushRunTarget(ctx, db.UpdatePushRunTargetParams{
					ID:         target.ID,
					Status:     "offline",
					FinishedAt: now,
				}); err != nil {
					slog.Error("failed to mark target offline",
						"target_id", target.ID, "error", err)
				}
				s.pushRouter.removeTarget(pushRun.ID, t.EnvoyID)
			}
		}

		// Queue promotable targets for stream promotion.
		for _, t := range promotableTargets {
			target, err := s.queries.CreatePushRunTarget(ctx, db.CreatePushRunTargetParams{
				PushRunID: pushRun.ID,
				EnvoyID:  t.EnvoyID,
				Hostname:  t.Hostname,
				Status:    "pending",
			})
			if err != nil {
				slog.Error("failed to create promotable push run target",
					"push_run_id", pushRun.ID, "envoy_id", t.EnvoyID, "error", err)
				continue
			}

			s.pushRouter.trackTarget(pushRun.ID, t.EnvoyID, target.ID)
			dispatched++

			msg := &pb.ServerStreamMessage{
				Msg: &pb.ServerStreamMessage_PushDispatch{
					PushDispatch: &pb.PushDispatch{
						PushId:         pushRun.ID,
						Command:        params.Command,
						TimeoutSeconds: timeoutSecs,
						Env:            pushEnv,
					},
				},
			}

			targetID := target.ID
			envoyID := t.EnvoyID
			pushRunID := pushRun.ID
			s.streamMgr.queueForPromotion(t.EnvoyID, msg, func() {
				if err := s.queries.UpdatePushRunTarget(context.Background(), db.UpdatePushRunTargetParams{
					ID:         targetID,
					Status:     "offline",
					FinishedAt: sql.NullTime{Time: time.Now(), Valid: true},
				}); err != nil {
					slog.Error("failed to mark promoted target offline",
						"target_id", targetID, "error", err)
				}
				s.pushRouter.removeTarget(pushRunID, envoyID)
				s.pushRouter.maybeFinishPushRun(context.Background(), pushRunID)
			})
		}

		// Update push run to running.
		if err := s.queries.UpdatePushRunStatus(ctx, db.UpdatePushRunStatusParams{
			ID:        pushRun.ID,
			Status:    "running",
			StartedAt: now,
		}); err != nil {
			slog.Error("failed to start push run", "push_run_id", pushRun.ID, "error", err)
		}

		// If no targets were dispatched, finalize immediately.
		if dispatched == 0 {
			s.pushRouter.maybeFinishPushRun(ctx, pushRun.ID)
		}
	}

	slog.Info("push dispatched",
		"push_run_id", pushRun.ID,
		"command", params.Command,
		"target_spec", params.TargetSpec,
		"total_targets", len(targets),
		"online", len(onlineTargets),
		"promotable", len(promotableTargets),
		"offline", len(offlineTargets),
		"rolling", rolling,
	)

	return &DispatchResult{ID: pushRun.ID, Status: "running"}, nil
}
