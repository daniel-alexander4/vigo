// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"log/slog"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/fleet"
	"github.com/hobnob/hobnob/server/metrics"
	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/webhook"
)

// handleDelta processes a DeltaEvent from the agent stream and returns an ack.
func (s *AgentService) handleDelta(thrallID, hostname string, delta *pb.DeltaEvent) *pb.DeltaAck {
	switch ev := delta.Event.(type) {
	case *pb.DeltaEvent_Run:
		s.handleRunDelta(thrallID, hostname, ev.Run)
	case *pb.DeltaEvent_Heartbeat:
		if s.fleetIndex != nil {
			s.fleetIndex.MarkSeen(thrallID, time.Now())
		}
	}
	return &pb.DeltaAck{AckedSequence: delta.Sequence}
}

// handleRunDelta processes a RunCompleted delta: updates FleetIndex, records
// metrics, dispatches webhooks/SMTP, and submits to the async RunBatcher.
func (s *AgentService) handleRunDelta(thrallID, hostname string, run *pb.RunCompleted) {
	runStatus := run.Status
	changedCount := int64(run.ChangedCount)
	failedCount := int64(run.FailedCount)
	skippedCount := int64(run.SkippedCount)

	// Update FleetIndex with run status + append to recent runs.
	now := time.Now()
	if s.fleetIndex != nil {
		s.fleetIndex.UpdateRunStatus(thrallID, runStatus, changedCount, now)
		s.fleetIndex.AppendRecentRun(fleet.RunSummary{
			RunID:        run.RunId,
			ThrallID:     thrallID,
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
			ThrallID:     thrallID,
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
		"thrall_id", thrallID,
		"status", runStatus,
		"changed", changedCount,
		"failed", failedCount,
		"skipped", skippedCount,
	)

	// Dispatch webhook event.
	if s.dispatcher != nil {
		event := webhook.FormatRunEvent(runStatus)
		s.dispatcher.Dispatch(nil, event, webhook.EventPayload{
			Thrall: &webhook.ThrallInfo{
				ID:       thrallID,
				Hostname: hostname,
			},
			Run: &webhook.RunInfo{
				ID:      run.RunId,
				Status:  runStatus,
				Changed: int32(changedCount),
				Failed:  int32(failedCount),
			},
		})

		if changedCount > 0 {
			s.dispatcher.Dispatch(nil, "drift.detected", webhook.EventPayload{
				Thrall: &webhook.ThrallInfo{
					ID:       thrallID,
					Hostname: hostname,
				},
				Run: &webhook.RunInfo{
					ID:      run.RunId,
					Status:  runStatus,
					Changed: int32(changedCount),
				},
			})
		}
	}

	// Send SMTP notification.
	smtpThrall := &hobnobsmtp.ThrallInfo{ID: thrallID, Hostname: hostname}
	smtpRun := &hobnobsmtp.RunInfo{ID: run.RunId, Status: runStatus, Changed: int32(changedCount), Failed: int32(failedCount)}
	s.smtpNotifier.Notify(hobnobsmtp.Event{
		Name: webhook.FormatRunEvent(runStatus), Timestamp: now.UTC(),
		Thrall: smtpThrall, Run: smtpRun,
	})
	if changedCount > 0 {
		s.smtpNotifier.Notify(hobnobsmtp.Event{
			Name: "drift.detected", Timestamp: now.UTC(),
			Thrall: smtpThrall, Run: smtpRun,
		})
	}
}
