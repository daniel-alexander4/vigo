// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
)

// runReport is one completed convergence run as either report path received
// it: the unary ReportResult RPC or a RunCompleted delta on a held stream.
type runReport struct {
	envoyID       string
	hostname      string
	results       []*pb.ResourceRunResult
	totalCount    int32
	traceID       string
	bundleVersion uint64 // policy bundle the run applied; 0 = not reported (unary)
	label         string // log message suffix distinguishing the path
}

// ingestRun is the single run-ingest path behind both ReportResult and the
// delta handler: one status/count derivation, one server-minted run id, one
// set of fleet-index updates, metrics and integration events. Only the write
// differs — persistSync records the run in the caller's goroutine (unary: the
// RPC fails if the write fails); otherwise it is queued on the RunBatcher (the
// stream path acks immediately). Either way the run id the FleetIndex links to
// is the id of the DB row.
func (s *AgentService) ingestRun(ctx context.Context, rep runReport, persistSync bool) error {
	resourceCount, changedCount, failedCount, blockedCount, skippedCount, runStatus :=
		summarizeResults(rep.results, rep.totalCount)

	// Attribute the run: "force-push" if the server force-pushed the bundle
	// this convergence applied, else "scheduled". One run per report, so the
	// consume is one-shot; reused for the FleetIndex ring + the DB row.
	trigger := "scheduled"
	if s.fleetIndex != nil {
		trigger = s.fleetIndex.ConsumeRunTrigger(rep.envoyID)
	}

	rec := buildRunRecord(rep, trigger, resourceCount, changedCount, failedCount, skippedCount, runStatus)
	runID := rec.Run.ID

	if persistSync {
		if _, err := s.runStore.RecordRun(ctx, rec); err != nil {
			slog.Error("failed to record run",
				"trace_id", rep.traceID,
				"count", len(rec.Results),
				"error", err,
			)
			return err
		}
	} else if s.runBatcher != nil && !s.runBatcher.Submit(rec) {
		slog.Debug("run batcher full; run history row shed", "run_id", runID, "envoy_id", rep.envoyID)
	}

	now := time.Now()
	if s.fleetIndex != nil {
		s.fleetIndex.UpdateRunStatus(rep.envoyID, runID, runStatus, changedCount, failedCount, resourceCount, now)
		s.fleetIndex.AppendRecentRun(fleet.RunSummary{
			RunID:        runID,
			EnvoyID:      rep.envoyID,
			Hostname:     rep.hostname,
			Status:       runStatus,
			Trigger:      trigger,
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			FinishedAt:   now,
		})
	}

	// Per-result metrics; label values are bounded inside metrics. No run
	// duration: the report carries no run start/finish time.
	s.metrics.RecordRunResults(rep.results)

	// Publish→converge latency: when this envoy first adopts a newer policy
	// version, observe how long it took from that version becoming current.
	// AdvanceConvergedVersion filters routine re-converges and initial
	// catch-up; VersionMintedAt returns !ok for versions older than its
	// retention horizon (catch-up to stale state, not a fresh publish wave).
	if v := rep.bundleVersion; v != 0 && s.fleetIndex != nil && s.fleetIndex.AdvanceConvergedVersion(rep.envoyID, v) {
		if s.configStore != nil {
			if minted, ok := s.configStore.VersionMintedAt(v); ok {
				s.metrics.PublishConvergence.Observe(now.Sub(minted).Seconds())
			}
		}
	}

	slog.Info("run completed"+rep.label,
		"run_id", runID,
		"envoy_id", rep.envoyID,
		"trace_id", rep.traceID,
		"status", runStatus,
		"changed", changedCount,
		"failed", failedCount,
		"blocked", blockedCount,
		"skipped", skippedCount,
	)

	s.dispatchRunEvents(rep, runID, runStatus, changedCount, failedCount, blockedCount, now)
	return nil
}

// buildRunRecord turns a report into the RecordRun write: a server-minted run
// id, one result row per reported resource, and a file-snapshot row per
// changed file (path + content_hash only — Rollback confirms the path was
// managed here; prior file content is never shipped or stored).
func buildRunRecord(rep runReport, trigger string, resourceCount, changedCount, failedCount, skippedCount int64, runStatus string) db.RecordRunParams {
	results := make([]db.CreateRunResultParams, 0, len(rep.results))
	var snaps []db.InsertFileSnapshotParams
	for _, r := range rep.results {
		errMsg := sql.NullString{}
		if r.GetErrorMessage() != "" {
			errMsg = sql.NullString{String: r.GetErrorMessage(), Valid: true}
		}
		var changed int64
		if r.GetChanged() {
			changed = 1
		}
		results = append(results, db.CreateRunResultParams{
			ConfigcrateName: r.GetConfigcrateName(),
			ResourceType:    r.GetResourceType(),
			ResourceName:    r.GetResourceName(),
			ActionTaken:     r.GetActionTaken(),
			Changed:         changed,
			ErrorMessage:    errMsg,
			DurationMs:      int64(r.GetDurationMs()),
		})
		if r.GetResourceType() == "file" && r.GetChanged() && r.GetFilePath() != "" {
			snaps = append(snaps, db.InsertFileSnapshotParams{
				EnvoyID:     rep.envoyID,
				FilePath:    r.GetFilePath(),
				ContentHash: r.GetContentHash(),
			})
		}
	}
	return db.RecordRunParams{
		Run: db.CreateRunParams{
			ID:            db.NewRunID(),
			EnvoyID:       rep.envoyID,
			Status:        convergence.RunStatusRunning,
			Trigger:       trigger,
			ResourceCount: resourceCount,
		},
		Results:       results,
		FileSnapshots: snaps,
		Finish: db.FinishRunParams{
			Status:       runStatus,
			ChangedCount: changedCount,
			FailedCount:  failedCount,
			SkippedCount: skippedCount,
		},
	}
}

// dispatchRunEvents fires the run.<status>, drift.detected and per-resource
// resource.blocked integration events for one ingested run.
func (s *AgentService) dispatchRunEvents(rep runReport, runID, runStatus string, changedCount, failedCount, blockedCount int64, now time.Time) {
	if s.integrations == nil {
		return
	}
	s.integrations.Dispatch(integrations.Event{
		Name:      formatRunEvent(runStatus),
		Timestamp: now,
		Hostname:  rep.hostname,
		EnvoyID:   rep.envoyID,
		Summary:   fmt.Sprintf("run %s: %d changed, %d failed (%d blocked)", runStatus, changedCount, failedCount, blockedCount),
		Details: map[string]any{
			"run_id":   runID,
			"trace_id": rep.traceID,
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
			Hostname:  rep.hostname,
			EnvoyID:   rep.envoyID,
			Summary:   fmt.Sprintf("%d resources changed on %s", changedCount, rep.hostname),
			Details:   map[string]any{"run_id": runID, "trace_id": rep.traceID, "changed": changedCount},
		})
	}

	for _, k := range s.blocked.observe(rep.envoyID, rep.results, blockedCount) {
		s.dispatchUnblocked(k, rep.hostname, rep.envoyID, runID, rep.traceID, now)
	}

	if blockedCount == 0 {
		return
	}
	for _, result := range rep.results {
		if result.GetActionTaken() != convergence.ActionTakenBlocked {
			continue
		}
		s.integrations.Dispatch(integrations.Event{
			Name:      "resource.blocked",
			Timestamp: now,
			Hostname:  rep.hostname,
			EnvoyID:   rep.envoyID,
			Summary:   fmt.Sprintf("%s/%s blocked: %s", result.GetConfigcrateName(), result.GetResourceName(), result.GetErrorMessage()),
			Details: map[string]any{
				"run_id":      runID,
				"configcrate": result.GetConfigcrateName(),
				"resource":    result.GetResourceName(),
				"trace_id":    rep.traceID,
				"error":       result.GetErrorMessage(),
			},
		})
	}
}

// dispatchUnblocked fires resource.unblocked, the resolve event for the
// fleet-wide resource.blocked incident on k. Its details carry the same
// configcrate/resource pair, so integrations.incidentKey matches the
// incident it resolves.
func (s *AgentService) dispatchUnblocked(k blockedKey, hostname, envoyID, runID, traceID string, now time.Time) {
	s.integrations.Dispatch(integrations.Event{
		Name:      "resource.unblocked",
		Timestamp: now,
		Hostname:  hostname,
		EnvoyID:   envoyID,
		Summary:   fmt.Sprintf("%s/%s no longer blocked on any envoy", k.configcrate, k.resource),
		Details: map[string]any{
			"run_id":      runID,
			"configcrate": k.configcrate,
			"resource":    k.resource,
			"trace_id":    traceID,
		},
	})
}

// forgetBlocked drops a departed envoy from the blocked tracker and resolves
// any incident it was the last holder of.
func (s *AgentService) forgetBlocked(envoyID string) {
	cleared := s.blocked.forget(envoyID)
	if s.integrations == nil {
		return
	}
	now := time.Now()
	for _, k := range cleared {
		s.dispatchUnblocked(k, "", envoyID, "", "", now)
	}
}

// formatRunEvent returns the event name for a run status (e.g., "run.success").
func formatRunEvent(status string) string {
	return fmt.Sprintf("run.%s", status)
}
