// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/metrics"
	vigosmtp "github.com/daniel-alexander4/vigo/server/smtp"
	"github.com/daniel-alexander4/vigo/server/webhook"
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
	var changedCount, failedCount, skippedCount int64
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
		if result.GetErrorMessage() != "" {
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

	// Compute run status: if any error -> "failure", else "success".
	runStatus := "success"
	if failedCount > 0 {
		runStatus = "failure"
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
		s.fleetIndex.UpdateRunStatus(envoyID, runID, runStatus, changedCount, now)
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
		"skipped", skippedCount,
	)

	// Dispatch webhook event asynchronously.
	if s.dispatcher != nil {
		event := webhook.FormatRunEvent(runStatus)
		s.dispatcher.Dispatch(ctx, event, webhook.EventPayload{
			Envoy: &webhook.EnvoyInfo{
				ID:       envoyID,
				Hostname: hostname,
			},
			Run: &webhook.RunInfo{
				ID:      runID,
				Status:  runStatus,
				Changed: int32(changedCount),
				Failed:  int32(failedCount),
			},
		})

		// Also fire drift.detected if any changes detected.
		if changedCount > 0 {
			s.dispatcher.Dispatch(ctx, "drift.detected", webhook.EventPayload{
				Envoy: &webhook.EnvoyInfo{
					ID:       envoyID,
					Hostname: hostname,
				},
				Run: &webhook.RunInfo{
					ID:      runID,
					Status:  runStatus,
					Changed: int32(changedCount),
				},
			})
		}
	}

	// Send SMTP notification.
	smtpEnvoy := &vigosmtp.EnvoyInfo{ID: envoyID, Hostname: hostname}
	smtpRun := &vigosmtp.RunInfo{ID: runID, Status: runStatus, Changed: int32(changedCount), Failed: int32(failedCount)}
	s.smtpNotifier.Notify(vigosmtp.Event{
		Name: webhook.FormatRunEvent(runStatus), Timestamp: time.Now().UTC(),
		Envoy: smtpEnvoy, Run: smtpRun,
	})
	if changedCount > 0 {
		s.smtpNotifier.Notify(vigosmtp.Event{
			Name: "drift.detected", Timestamp: time.Now().UTC(),
			Envoy: smtpEnvoy, Run: smtpRun,
		})
	}

	return &pb.ReportResultResponse{Acknowledged: true}, nil
}
