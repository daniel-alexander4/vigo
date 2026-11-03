// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
)

// StateFlusher reads dirty envoys from the FleetIndex and batch-writes their
// state changes (last_seen, force_push, pending_rotated_secrets) to SQLite
// periodically. It replaces the LastSeenBatcher with a more comprehensive
// flush that covers all mutable envoy fields.
type StateFlusher struct {
	fleet   *FleetIndex
	queries *db.Queries
	metrics *metrics.Metrics
}

// NewStateFlusher creates a flusher that writes dirty state from the FleetIndex
// to SQLite. m owns the FleetIndex flush gauges/histograms.
func NewStateFlusher(fleet *FleetIndex, queries *db.Queries, m *metrics.Metrics) *StateFlusher {
	return &StateFlusher{
		fleet:   fleet,
		queries: queries,
		metrics: m,
	}
}

// Start runs the flush loop, writing dirty envoy state to SQLite every interval.
// Blocks until ctx is cancelled. Performs a final flush on shutdown. The offset
// parameter staggers the start relative to other flushers to avoid SQLite write
// lock contention.
func (sf *StateFlusher) Start(ctx context.Context, interval, offset time.Duration) {
	if offset > 0 {
		select {
		case <-time.After(offset):
		case <-ctx.Done():
			sf.Flush(context.Background())
			return
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sf.Flush(context.Background())
			return
		case <-ticker.C:
			daemon.Guard("state-flusher", func() { sf.Flush(ctx) })
		}
	}
}

// Flush drains dirty envoys and writes their state to SQLite in batches.
func (sf *StateFlusher) Flush(ctx context.Context) {
	drainStart := time.Now()
	dirty := sf.fleet.DrainDirty()
	drainDur := time.Since(drainStart)
	if len(dirty) == 0 {
		return
	}
	// Observed only on non-empty drains — empty drains complete in <1µs
	// and aren't a useful signal. Mirrors the existing FlushDuration
	// pattern which is also conditional on a non-empty set.
	sf.metrics.FleetIndexDrainDirtyLock.Observe(drainDur.Seconds())

	start := time.Now()
	sf.metrics.FleetIndexDirtyCount.Set(float64(len(dirty)))
	defer func() {
		sf.metrics.FleetIndexFlushDuration.Observe(time.Since(start).Seconds())
	}()

	// Collect IDs for batch last_seen update.
	var lastSeenIDs []string
	for id := range dirty {
		lastSeenIDs = append(lastSeenIDs, id)
	}

	// Batch update last_seen in chunks of 5000. SQLite's variable-parameter
	// limit is 32766 (3.32+); 5000 is well under that and collapses 50
	// SQL statements per 25K-envoy flush into 5, each acquiring the WAL
	// writer mutex exactly once. The 2026-05-25 loadtest showed 500-row
	// chunks taking 50–950 ms each — 10× fewer chunks is the easy win.
	const chunkSize = 5000
	// Per-chunk SQLite WAL-write timing: log individual chunks that exceed
	// the threshold so we can see whether the request-tail spikes correlate
	// with specific slow writes vs. uniformly slow ones (uniform = WAL
	// queue depth; one specific = a particular statement is the bottleneck).
	// Threshold deliberately lower than the per-flush slow threshold so a
	// 200ms chunk shows up even when the flush as a whole is "fast".
	const slowChunkThreshold = 50 * time.Millisecond
	var lastSeenFailed bool
	batchStart := time.Now()
	for i := 0; i < len(lastSeenIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(lastSeenIDs) {
			end = len(lastSeenIDs)
		}
		chunkStart := time.Now()
		if err := sf.queries.BatchUpdateLastSeen(ctx, lastSeenIDs[i:end]); err != nil {
			slog.Error("state flusher: batch last_seen failed", "count", end-i, "error", err)
			lastSeenFailed = true
		}
		if d := time.Since(chunkStart); d >= slowChunkThreshold {
			slog.Info("state flusher: slow chunk", "op", "batch_last_seen", "count", end-i, "duration", d)
		}
	}
	batchDur := time.Since(batchStart)

	// Collect ids per verb. The old shape issued 3 separate UPDATEs per
	// dirty envoy unconditionally (9000 statements at 3000 dirty envoys),
	// which serialized through the SQLite WAL writer mutex — measured at
	// 78s for a single 3000-envoy flush in the 0.67.22 load test. Batching
	// each verb into one chunked WHERE-id-IN(...) statement collapses that
	// to a handful of statements; the trailing `AND <field> != current`
	// predicate in each batch query (see server/db/batch.go) makes SQLite
	// skip rows that don't actually need a write, so the common case
	// (mostly-clean dirty set) becomes a near-instant no-op.
	flagsStart := time.Now()
	var (
		clearForcePushIDs       []string
		clearRotatedSecretsIDs  []string
		clearMaintenanceIDs     []string
		setMaintenanceIDs       []string
		setMaintenanceVals      []time.Time
	)
	for id, ts := range dirty {
		if !ts.ForcePush {
			clearForcePushIDs = append(clearForcePushIDs, id)
		}
		if len(ts.PendingRotatedSecrets) == 0 {
			clearRotatedSecretsIDs = append(clearRotatedSecretsIDs, id)
		}
		if ts.MaintenanceUntil.IsZero() {
			clearMaintenanceIDs = append(clearMaintenanceIDs, id)
		} else {
			setMaintenanceIDs = append(setMaintenanceIDs, id)
			setMaintenanceVals = append(setMaintenanceVals, ts.MaintenanceUntil)
		}
	}
	// Value-grouped agent_version persistence. The per-checkin UpdateAgentVersion
	// call was retired (server/grpc/checkin.go + agentstream.go) to avoid
	// serializing N single-row UPDATEs through the WAL writer mutex during
	// stagger-up. Group dirty envoys by their reported version; most of the
	// fleet shares a small number of distinct versions at any time so this
	// collapses 10K statements into ~chunkCount-per-version batched UPDATEs.
	// The BatchUpdateAgentVersion query carries `AND agent_version != ?` so
	// no-op rows skip the row write — steady-state ticks pay only WHERE
	// evaluation, not row IO.
	agentVersionGroups := make(map[string][]string)
	for id, ts := range dirty {
		if ts.AgentVersion == "" {
			continue
		}
		agentVersionGroups[ts.AgentVersion] = append(agentVersionGroups[ts.AgentVersion], id)
	}

	chunkedBatch := func(ids []string, name string, fn func(context.Context, []string) error) {
		for i := 0; i < len(ids); i += chunkSize {
			end := i + chunkSize
			if end > len(ids) {
				end = len(ids)
			}
			chunkStart := time.Now()
			if err := fn(ctx, ids[i:end]); err != nil {
				slog.Error("state flusher: batch failed", "op", name, "count", end-i, "error", err)
			}
			if d := time.Since(chunkStart); d >= slowChunkThreshold {
				slog.Info("state flusher: slow chunk", "op", name, "count", end-i, "duration", d)
			}
		}
	}
	chunkedBatch(clearForcePushIDs, "clear_force_push", sf.queries.BatchClearForcePush)
	chunkedBatch(clearRotatedSecretsIDs, "clear_rotated_secrets", sf.queries.BatchClearRotatedSecrets)
	chunkedBatch(clearMaintenanceIDs, "clear_maintenance", sf.queries.BatchClearEnvoyMaintenance)
	for version, ids := range agentVersionGroups {
		for i := 0; i < len(ids); i += chunkSize {
			end := i + chunkSize
			if end > len(ids) {
				end = len(ids)
			}
			chunkStart := time.Now()
			if err := sf.queries.BatchUpdateAgentVersion(ctx, ids[i:end], version); err != nil {
				slog.Error("state flusher: batch agent_version failed", "version", version, "count", end-i, "error", err)
			}
			if d := time.Since(chunkStart); d >= slowChunkThreshold {
				slog.Info("state flusher: slow chunk", "op", "batch_agent_version", "version", version, "count", end-i, "duration", d)
			}
		}
	}
	// Operator-set maintenance windows are rare; per-envoy fallback for the
	// non-NULL case keeps the batched path simple and avoids the CASE-WHEN
	// gymnastics needed for divergent per-row values.
	for i, id := range setMaintenanceIDs {
		if err := sf.queries.UpdateEnvoyMaintenance(ctx, db.UpdateEnvoyMaintenanceParams{
			MaintenanceUntil: sql.NullTime{Time: setMaintenanceVals[i], Valid: true},
			ID:               id,
		}); err != nil {
			slog.Error("state flusher: set maintenance failed", "envoy_id", id, "error", err)
		}
	}

	flagsDur := time.Since(flagsStart)

	// Re-mark failed envoys as dirty for next flush.
	if lastSeenFailed {
		for _, id := range lastSeenIDs {
			sf.fleet.MarkDirty(id)
		}
	}

	// Per-flush breakdown — surfaces the unbatched-per-envoy-UPDATE shape
	// that dominates wall time at fleet sizes where many envoys are
	// dirty in the same tick (3× UPDATE per envoy in the flags loop).
	// Promoted from Debug to Info above a slow-flush threshold so the
	// signal lands in journald during load events without noise in
	// steady state.
	const slowFlushThreshold = 100 * time.Millisecond
	total := drainDur + batchDur + flagsDur
	if total >= slowFlushThreshold {
		slog.Warn("state flusher: slow flush",
			"count", len(dirty),
			"total", total,
			"drain", drainDur,
			"batch_last_seen", batchDur,
			"per_envoy_flags", flagsDur,
		)
	} else {
		slog.Debug("state flusher: flushed",
			"count", len(dirty),
			"total", total,
			"drain", drainDur,
			"batch_last_seen", batchDur,
			"per_envoy_flags", flagsDur,
		)
	}
}
