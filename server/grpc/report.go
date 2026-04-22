// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"fmt"

	"github.com/google/uuid"
	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/metrics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportResult handles the VigoAgent.ReportResult RPC. It stores per-module
// execution results in the database and marks the run as finished.
func (s *AgentService) ReportResult(ctx context.Context, req *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := req.GetEnvoyId()

	// Create the run record (server generates the UUID).
	run, err := s.runStore.CreateRun(ctx, db.CreateRunParams{
		EnvoyID:      envoyID,
		Status:      "running",
		ModuleCount: int64(len(req.GetResults())),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating run: %v", err)
	}
	runID := run.ID

	// Build batch of results and compute counts in a single pass.
	var changedCount, failedCount, skippedCount, blockedCount int64
	batch := make([]db.CreateRunResultParams, 0, len(req.GetResults()))
	for _, result := range req.GetResults() {
		errMsg := sql.NullString{}
		if result.GetErrorMessage() != "" {
			errMsg = sql.NullString{String: result.GetErrorMessage(), Valid: true}
		}

		var changed int64
		if result.GetChanged() {
			changed = 1
		}

		batch = append(batch, db.CreateRunResultParams{
			RunID:        runID,
			ModuleName:   result.GetModuleName(),
			ResourceType: result.GetResourceType(),
			ResourceName: result.GetResourceName(),
			ActionTaken:  result.GetActionTaken(),
			Changed:      changed,
			ErrorMessage: errMsg,
			DurationMs:   int64(result.GetDurationMs()),
		})

		if result.GetChanged() {
			changedCount++
		}
		if result.GetActionTaken() == "blocked" {
			blockedCount++
		} else if result.GetErrorMessage() != "" {
			failedCount++
		}
		if result.GetActionTaken() == "settled" && !result.GetChanged() {
			skippedCount++
		}
	}

	// Insert all results in a single batched operation.
	if err := s.runStore.BatchCreateRunResults(ctx, batch); err != nil {
		slog.Error("failed to store run results",
			"run_id", runID,
			"count", len(batch),
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "storing run results: %v", err)
	}

	// Compute run status: failures → "failure", blocked-only → "blocked", else "success".
	runStatus := "success"
	if failedCount > 0 {
		runStatus = "failure"
	} else if blockedCount > 0 {
		runStatus = "blocked"
	}

	// Finish the run with computed counts.
	err = s.runStore.FinishRun(ctx, db.FinishRunParams{
		ID:           runID,
		Status:       runStatus,
		ChangedCount: changedCount,
		FailedCount:  failedCount,
		SkippedCount: skippedCount,
	})
	if err != nil {
		slog.Error("failed to finish run",
			"run_id", runID,
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "finishing run: %v", err)
	}

	// Reuse envoy from interceptor context (avoids redundant DB lookup).
	hostname := envoyID // fallback
	if n := db.EnvoyFromContext(ctx); n != nil {
		hostname = n.Hostname
	}

	// Record metrics for each result.
	for _, result := range req.GetResults() {
		metrics.ModuleActions.WithLabelValues(result.GetResourceType(), result.GetActionTaken()).Inc()
		if result.GetChanged() {
			metrics.DriftDetected.WithLabelValues(hostname, result.GetModuleName()).Inc()
		}
	}

	// Record run duration (use the run we already created — no re-fetch needed).
	duration := time.Since(run.StartedAt).Seconds()
	metrics.RunDuration.WithLabelValues(hostname).Observe(duration)

	// Update FleetIndex with run status + append to recent runs.
	if s.fleetIndex != nil {
		now := time.Now()
		moduleCount := int64(len(req.GetResults()))
		s.fleetIndex.UpdateRunStatus(envoyID, runID, runStatus, changedCount, failedCount, moduleCount, now)
		s.fleetIndex.AppendRecentRun(fleet.RunSummary{
			RunID:        runID,
			EnvoyID:     envoyID,
			Hostname:     hostname,
			Status:       runStatus,
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			FinishedAt:   now,
		})
	}

	// Capture file snapshots (previous content before overwrite) for rollback support.
	// Skip files larger than 1 MiB to avoid bloating the DB.
	const maxSnapshotSize = 1 << 20
	for _, result := range req.GetResults() {
		if result.GetResourceType() != "file" || len(result.GetPreviousContent()) == 0 {
			continue
		}
		if len(result.GetPreviousContent()) > maxSnapshotSize {
			continue
		}
		if err := s.queries.InsertFileSnapshot(ctx, db.InsertFileSnapshotParams{
			EnvoyID:    envoyID,
			FilePath:    result.GetFilePath(),
			ContentHash: result.GetContentHash(),
			Content:     result.GetPreviousContent(),
			RunID:       sql.NullString{String: runID, Valid: true},
		}); err != nil {
			slog.Error("failed to save file snapshot",
				"run_id", runID,
				"file_path", result.GetFilePath(),
				"error", err,
			)
		}
	}

	slog.Info("run completed",
		"run_id", runID,
		"envoy_id", envoyID,
		"status", runStatus,
		"changed", changedCount,
		"failed", failedCount,
		"blocked", blockedCount,
		"skipped", skippedCount,
	)

	// Dispatch events to integrations.
	if s.integrations != nil {
		s.integrations.Dispatch(integrations.Event{
			Name:      formatRunEvent(runStatus),
			Timestamp: time.Now(),
			Hostname:  hostname,
			EnvoyID:   envoyID,
			Summary:   fmt.Sprintf("run %s: %d changed, %d failed, %d blocked", runStatus, changedCount, failedCount, blockedCount),
			Details: map[string]any{
				"run_id":  runID,
				"status":  runStatus,
				"changed": changedCount,
				"failed":  failedCount,
				"blocked": blockedCount,
			},
		})

		if changedCount > 0 {
			s.integrations.Dispatch(integrations.Event{
				Name:      "drift.detected",
				Timestamp: time.Now(),
				Hostname:  hostname,
				EnvoyID:   envoyID,
				Summary:   fmt.Sprintf("%d resources changed on %s", changedCount, hostname),
				Details:   map[string]any{"run_id": runID, "changed": changedCount},
			})
		}

		// Fire resource.blocked for each blocked resource in this run.
		if blockedCount > 0 {
			for _, result := range req.GetResults() {
				if result.GetActionTaken() == "blocked" {
					s.integrations.Dispatch(integrations.Event{
						Name:      "resource.blocked",
						Timestamp: time.Now(),
						Hostname:  hostname,
						EnvoyID:   envoyID,
						Summary:   fmt.Sprintf("%s/%s blocked: %s", result.GetModuleName(), result.GetResourceName(), result.GetErrorMessage()),
						Details: map[string]any{
							"module":   result.GetModuleName(),
							"resource": result.GetResourceName(),
							"error":    result.GetErrorMessage(),
						},
					})
				}
			}
		}
	}

	return &pb.ReportResultResponse{Acknowledged: true}, nil
}

// formatRunEvent returns the event name for a run status (e.g., "run.success").
func formatRunEvent(status string) string {
	return fmt.Sprintf("run.%s", status)
}
