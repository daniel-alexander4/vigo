// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"fmt"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportResult handles the VigoAgent.ReportResult RPC. It stores per-module
// execution results in the database and marks the run as finished.
func (s *AgentService) ReportResult(ctx context.Context, req *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	// Admission control: shed before doing any work so a result storm
	// (e.g. fleet-wide reporting after a publish) can't pile unbounded
	// writes onto the single SQLite writer. ResourceExhausted tells the
	// agent to back off — it already does.
	if !s.tryAcquireReportSlot() {
		return nil, status.Errorf(codes.ResourceExhausted, "server overloaded (report admission cap %d reached); retry with backoff", cap(s.reportResultSem))
	}
	defer s.releaseReportSlot()

	envoyID := req.GetEnvoyId()
	var traceID string
	ctx, traceID = EnsureTraceID(ctx, req.GetTraceparent())

	// Attribute the run: "force-push" if the server force-pushed the
	// bundle this convergence applied, else "scheduled". One run per
	// report, so the consume is one-shot.
	trigger := "scheduled"
	if s.fleetIndex != nil {
		trigger = s.fleetIndex.ConsumeRunTrigger(envoyID)
	}

	// Canonical counts + status from (results, total_count) — the same blocked-aware
	// derivation the delta path uses (summarizeResults). Newer agents elide settled
	// no-ops and report the real total in total_count; older agents send every row
	// and leave total_count 0, so it falls back to len(results).
	resourceCount, changedCount, failedCount, blockedCount, skippedCount, runStatus :=
		summarizeResults(req.GetResults(), req.GetTotalCount())

	// Build the per-result DB rows. RunID is stamped by RecordRun from the run it
	// creates, so it's left blank here.
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
			ConfigcrateName: result.GetConfigcrateName(),
			ResourceType:    result.GetResourceType(),
			ResourceName:    result.GetResourceName(),
			ActionTaken:     result.GetActionTaken(),
			Changed:         changed,
			ErrorMessage:    errMsg,
			DurationMs:      int64(result.GetDurationMs()),
		})
	}

	// Persist the whole run atomically (run row + results + finish) in one
	// transaction on the single writer — no orphaned "running" row if a write
	// fails partway, and one write-lock acquisition instead of three.
	run, err := s.runStore.RecordRun(ctx, db.RecordRunParams{
		Run: db.CreateRunParams{
			EnvoyID:       envoyID,
			Status:        "running",
			Trigger:       trigger,
			ResourceCount: resourceCount,
		},
		Results: batch,
		Finish: db.FinishRunParams{
			Status:       runStatus,
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			SkippedCount: skippedCount,
		},
	})
	if err != nil {
		slog.Error("failed to record run",
			"trace_id", traceID,
			"count", len(batch),
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "recording run: %v", err)
	}
	runID := run.ID

	// Reuse envoy from interceptor context (avoids redundant DB lookup).
	hostname := envoyID // fallback
	if n := db.EnvoyFromContext(ctx); n != nil {
		hostname = n.Hostname
	}

	// Record metrics for each result.
	for _, result := range req.GetResults() {
		s.metrics.ConfigcrateActions.WithLabelValues(result.GetResourceType(), result.GetActionTaken()).Inc()
		if result.GetChanged() {
			s.metrics.DriftDetected.WithLabelValues(hostname, result.GetConfigcrateName()).Inc()
		}
	}

	// Record run duration (use the run we already created — no re-fetch needed).
	duration := time.Since(run.StartedAt).Seconds()
	s.metrics.RunDuration.WithLabelValues(hostname).Observe(duration)

	// Update FleetIndex with run status + append to recent runs.
	if s.fleetIndex != nil {
		now := time.Now()
		s.fleetIndex.UpdateRunStatus(envoyID, runID, runStatus, changedCount, failedCount, resourceCount, now)
		s.fleetIndex.AppendRecentRun(fleet.RunSummary{
			RunID:        runID,
			EnvoyID:      envoyID,
			Hostname:     hostname,
			Status:       runStatus,
			Trigger:      trigger,
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			FinishedAt:   now,
		})
	}

	// Record a file-snapshot row for each changed file, so a later Rollback can
	// confirm the path was managed here and force-push the desired policy. Only
	// path + content_hash are stored — the agent no longer ships prior file
	// content (a plaintext-secret leak that nothing read back).
	for _, result := range req.GetResults() {
		if result.GetResourceType() != "file" || !result.GetChanged() || result.GetFilePath() == "" {
			continue
		}
		if err := s.queries.InsertFileSnapshot(ctx, db.InsertFileSnapshotParams{
			EnvoyID:     envoyID,
			FilePath:    result.GetFilePath(),
			ContentHash: result.GetContentHash(),
			RunID:       sql.NullString{String: runID, Valid: true},
		}); err != nil {
			slog.Error("failed to save file snapshot",
				"run_id", runID,
				"trace_id", traceID,
				"file_path", result.GetFilePath(),
				"error", err,
			)
		}
	}

	slog.Info("run completed",
		"run_id", runID,
		"envoy_id", envoyID,
		"trace_id", traceID,
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
				"run_id":   runID,
				"trace_id": traceID,
				"status":   runStatus,
				"changed":  changedCount,
				"failed":   failedCount,
				"blocked":  blockedCount,
			},
		})

		if changedCount > 0 {
			s.integrations.Dispatch(integrations.Event{
				Name:      "drift.detected",
				Timestamp: time.Now(),
				Hostname:  hostname,
				EnvoyID:   envoyID,
				Summary:   fmt.Sprintf("%d resources changed on %s", changedCount, hostname),
				Details:   map[string]any{"run_id": runID, "trace_id": traceID, "changed": changedCount},
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
						Summary:   fmt.Sprintf("%s/%s blocked: %s", result.GetConfigcrateName(), result.GetResourceName(), result.GetErrorMessage()),
						Details: map[string]any{
							"configcrate": result.GetConfigcrateName(),
							"resource":    result.GetResourceName(),
							"trace_id":    traceID,
							"error":       result.GetErrorMessage(),
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
