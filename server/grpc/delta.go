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
	// Canonical counts + status from (results, total_count) — one blocked-aware
	// derivation shared with the unary ReportResult path. The agent sends only
	// non-no-op rows (blocked retained) + the true total; everything else is
	// derived here, so a blocked resource surfaces as status="blocked" instead of
	// being miscounted as skipped.
	moduleCount, changedCount, failedCount, blockedCount, skippedCount, runStatus :=
		summarizeResults(run.Results, run.TotalCount)
	_, traceID := ParseOrGenerate(run.GetTraceparent())

	// Attribute the run: "force-push" if the server force-pushed the
	// bundle this convergence applied, else "scheduled". Consumed once
	// here (one-shot) and reused for the FleetIndex ring + the DB row.
	trigger := "scheduled"
	if s.fleetIndex != nil {
		trigger = s.fleetIndex.ConsumeRunTrigger(envoyID)
	}

	// Update FleetIndex with run status + append to recent runs.
	now := time.Now()
	if s.fleetIndex != nil {
		s.fleetIndex.UpdateRunStatus(envoyID, run.RunId, runStatus, changedCount, failedCount, moduleCount, now)
		s.fleetIndex.AppendRecentRun(fleet.RunSummary{
			RunID:        run.RunId,
			EnvoyID:      envoyID,
			Hostname:     hostname,
			Status:       runStatus,
			Trigger:      trigger,
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			FinishedAt:   now,
		})
	}

	// Record metrics.
	for _, result := range run.Results {
		s.metrics.ConfigcrateActions.WithLabelValues(result.GetResourceType(), result.GetActionTaken()).Inc()
		if result.GetChanged() {
			s.metrics.DriftDetected.WithLabelValues(hostname, result.GetConfigcrateName()).Inc()
		}
	}
	s.metrics.RunDuration.WithLabelValues(hostname).Observe(0) // delta doesn't carry duration

	// Publish→converge latency: when this envoy first adopts a newer policy
	// version, observe how long it took from that version becoming current.
	// AdvanceConvergedVersion filters routine re-converges and initial
	// catch-up; VersionMintedAt returns !ok for versions older than its
	// retention horizon (catch-up to stale state, not a fresh publish wave).
	if v := run.BundleVersion; v != 0 && s.fleetIndex != nil && s.fleetIndex.AdvanceConvergedVersion(envoyID, v) {
		if s.configStore != nil {
			if minted, ok := s.configStore.VersionMintedAt(v); ok {
				s.metrics.PublishConvergence.Observe(now.Sub(minted).Seconds())
			}
		}
	}

	// Build DB result params and submit to async batcher.
	if s.runBatcher != nil {
		var dbResults []fleet.RunBatchResultParam
		for _, r := range run.Results {
			dbResults = append(dbResults, fleet.RunBatchResultParam{
				ConfigcrateName: r.GetConfigcrateName(),
				ResourceType:    r.GetResourceType(),
				ResourceName:    r.GetResourceName(),
				ActionTaken:     r.GetActionTaken(),
				Changed:         r.GetChanged(),
				ErrorMessage:    r.GetErrorMessage(),
				DurationMs:      r.GetDurationMs(),
			})
		}

		s.runBatcher.Submit(fleet.RunBatchEntry{
			RunID:         run.RunId,
			EnvoyID:       envoyID,
			Hostname:      hostname,
			Status:        runStatus,
			Trigger:       trigger,
			ResourceCount: moduleCount,
			ChangedCount:  changedCount,
			FailedCount:   failedCount,
			SkippedCount:  skippedCount,
			DBResults:     dbResults,
		})
	}

	slog.Info("run completed (delta)",
		"run_id", run.RunId,
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
			Timestamp: now,
			Hostname:  hostname,
			EnvoyID:   envoyID,
			Summary:   fmt.Sprintf("run %s: %d changed, %d failed, %d blocked", runStatus, changedCount, failedCount, blockedCount),
			Details: map[string]any{
				"run_id":   run.RunId,
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
				Timestamp: now,
				Hostname:  hostname,
				EnvoyID:   envoyID,
				Summary:   fmt.Sprintf("%d resources changed on %s", changedCount, hostname),
				Details:   map[string]any{"run_id": run.RunId, "trace_id": traceID, "changed": changedCount},
			})
		}

		// Fire resource.blocked for each blocked resource — mirrors the unary
		// ReportResult path so guardrail blocks surface on the delta path too.
		if blockedCount > 0 {
			for _, result := range run.Results {
				if result.GetActionTaken() != "blocked" {
					continue
				}
				s.integrations.Dispatch(integrations.Event{
					Name:      "resource.blocked",
					Timestamp: now,
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
