// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"fmt"
	"log/slog"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/metrics"
)

// handleDelta processes a DeltaEvent from the agent stream and returns an ack.
func (s *AgentService) handleDelta(envoyID, hostname string, delta *pb.DeltaEvent) *pb.DeltaAck {
	switch ev := delta.Event.(type) {
	case *pb.DeltaEvent_Run:
		s.handleRunDelta(envoyID, hostname, ev.Run)
	case *pb.DeltaEvent_Heartbeat:
		// Heartbeats only occur within streams — update in-memory only to avoid
		// redundant last_seen DB writes. The stream disconnect handler persists
		// the final timestamp.
		if s.fleetIndex != nil {
			s.fleetIndex.MarkSeenInMemory(envoyID, time.Now())
		}
	}
	return &pb.DeltaAck{AckedSequence: delta.Sequence}
}

// handleRunDelta processes a RunCompleted delta: updates FleetIndex, records
// metrics, dispatches webhooks/SMTP, and submits to the async RunBatcher.
func (s *AgentService) handleRunDelta(envoyID, hostname string, run *pb.RunCompleted) {
	runStatus := run.Status
	changedCount := int64(run.ChangedCount)
	failedCount := int64(run.FailedCount)
	skippedCount := int64(run.SkippedCount)
	moduleCount := int64(run.TotalCount)

	// Update FleetIndex with run status + append to recent runs.
	now := time.Now()
	if s.fleetIndex != nil {
		s.fleetIndex.UpdateRunStatus(envoyID, run.RunId, runStatus, changedCount, failedCount, moduleCount, now)
		s.fleetIndex.AppendRecentRun(fleet.RunSummary{
			RunID:        run.RunId,
			EnvoyID:     envoyID,
			Hostname:     hostname,
			Status:       runStatus,
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			FinishedAt:   now,
		})
	}

	// Record metrics.
	for _, result := range run.Results {
		metrics.ModuleActions.WithLabelValues(result.GetResourceType(), result.GetActionTaken()).Inc()
		if result.GetChanged() {
			metrics.DriftDetected.WithLabelValues(hostname, result.GetModuleName()).Inc()
		}
	}
	metrics.RunDuration.WithLabelValues(hostname).Observe(0) // delta doesn't carry duration

	// Build DB result params and submit to async batcher.
	if s.runBatcher != nil {
		var dbResults []fleet.RunBatchResultParam
		for _, r := range run.Results {
			dbResults = append(dbResults, fleet.RunBatchResultParam{
				ModuleName:   r.GetModuleName(),
				ResourceType: r.GetResourceType(),
				ResourceName: r.GetResourceName(),
				ActionTaken:  r.GetActionTaken(),
				Changed:      r.GetChanged(),
				ErrorMessage: r.GetErrorMessage(),
				DurationMs:   r.GetDurationMs(),
			})
		}

		s.runBatcher.Submit(fleet.RunBatchEntry{
			RunID:        run.RunId,
			EnvoyID:     envoyID,
			Hostname:     hostname,
			Status:       runStatus,
			ModuleCount:  int64(run.TotalCount),
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			SkippedCount: skippedCount,
			DBResults:    dbResults,
		})
	}

	slog.Info("run completed (delta)",
		"run_id", run.RunId,
		"envoy_id", envoyID,
		"status", runStatus,
		"changed", changedCount,
		"failed", failedCount,
		"skipped", skippedCount,
	)

	// Dispatch events to integrations.
	if s.integrations != nil {
		s.integrations.Dispatch(integrations.Event{
			Name:      formatRunEvent(runStatus),
			Timestamp: now,
			Hostname:  hostname,
			EnvoyID:   envoyID,
			Summary:   fmt.Sprintf("run %s: %d changed, %d failed", runStatus, changedCount, failedCount),
			Details: map[string]any{
				"run_id":  run.RunId,
				"status":  runStatus,
				"changed": changedCount,
				"failed":  failedCount,
			},
		})

		if changedCount > 0 {
			s.integrations.Dispatch(integrations.Event{
				Name:      "drift.detected",
				Timestamp: now,
				Hostname:  hostname,
				EnvoyID:   envoyID,
				Summary:   fmt.Sprintf("%d resources changed on %s", changedCount, hostname),
				Details:   map[string]any{"run_id": run.RunId, "changed": changedCount},
			})
		}
	}
}
