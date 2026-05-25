// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
)

// StateFlusher reads dirty envoys from the FleetIndex and batch-writes their
// state changes (last_seen, force_push, pending_rotated_secrets) to SQLite
// periodically. All writes for one flush tick run in a single transaction —
// one COMMIT (one fsync) instead of one per chunked UPDATE — which kept
// batch_last_seen at 47s for a 10000-envoy dirty set before the wrap.
type StateFlusher struct {
	fleet   *FleetIndex
	sqlDB   *sql.DB
	queries *db.Queries
	metrics *metrics.Metrics
}

// NewStateFlusher creates a flusher that writes dirty state from the FleetIndex
// to SQLite. sqlDB is used to open the per-flush transaction; queries is the
// non-tx Queries handle (tx-wrapped via WithTx inside each Flush call).
// m owns the FleetIndex flush gauges/histograms.
func NewStateFlusher(fleet *FleetIndex, sqlDB *sql.DB, queries *db.Queries, m *metrics.Metrics) *StateFlusher {
	return &StateFlusher{
		fleet:   fleet,
		sqlDB:   sqlDB,
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
			sf.Flush(ctx)
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

	// All writes for this flush tick go in one tx. One COMMIT (one fsync)
	// instead of one per chunked UPDATE. All-or-nothing: any sub-call
	// failure rolls back, and the deferred re-mark-dirty below puts every
	// drained id back on the dirty set so the next tick retries.
	tx, err := sf.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("state flusher: begin tx failed", "error", err)
		for _, id := range lastSeenIDs {
			sf.fleet.MarkDirty(id)
		}
		return
	}
	defer tx.Rollback() //nolint:errcheck // rollback after Commit is a no-op
	qtx := sf.queries.WithTx(tx)

	// Batch update last_seen in chunks of 500.
	const chunkSize = 500
	var txFailed bool
	batchStart := time.Now()
	for i := 0; i < len(lastSeenIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(lastSeenIDs) {
			end = len(lastSeenIDs)
		}
		if err := qtx.BatchUpdateLastSeen(ctx, lastSeenIDs[i:end]); err != nil {
			slog.Error("state flusher: batch last_seen failed", "count", end-i, "error", err)
			txFailed = true
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
	chunkedBatch := func(ids []string, name string, fn func(context.Context, []string) error) {
		for i := 0; i < len(ids); i += chunkSize {
			end := i + chunkSize
			if end > len(ids) {
				end = len(ids)
			}
			if err := fn(ctx, ids[i:end]); err != nil {
				slog.Error("state flusher: batch failed", "op", name, "count", end-i, "error", err)
				txFailed = true
			}
		}
	}
	chunkedBatch(clearForcePushIDs, "clear_force_push", qtx.BatchClearForcePush)
	chunkedBatch(clearRotatedSecretsIDs, "clear_rotated_secrets", qtx.BatchClearRotatedSecrets)
	chunkedBatch(clearMaintenanceIDs, "clear_maintenance", qtx.BatchClearEnvoyMaintenance)
	// Operator-set maintenance windows are rare; per-envoy fallback for the
	// non-NULL case keeps the batched path simple and avoids the CASE-WHEN
	// gymnastics needed for divergent per-row values.
	for i, id := range setMaintenanceIDs {
		if err := qtx.UpdateEnvoyMaintenance(ctx, db.UpdateEnvoyMaintenanceParams{
			MaintenanceUntil: sql.NullTime{Time: setMaintenanceVals[i], Valid: true},
			ID:               id,
		}); err != nil {
			slog.Error("state flusher: set maintenance failed", "envoy_id", id, "error", err)
			txFailed = true
		}
	}

	flagsDur := time.Since(flagsStart)

	// Commit (one fsync) or roll back. On rollback, every drained id goes
	// back on the dirty set so the next tick retries.
	if txFailed {
		for _, id := range lastSeenIDs {
			sf.fleet.MarkDirty(id)
		}
	} else if err := tx.Commit(); err != nil {
		slog.Error("state flusher: commit failed", "error", err)
		for _, id := range lastSeenIDs {
			sf.fleet.MarkDirty(id)
		}
		txFailed = true
	}

	// Per-flush breakdown — surfaces the unbatched-per-envoy-UPDATE shape
	// that dominates wall time at fleet sizes where many envoys are
	// dirty in the same tick (3× UPDATE per envoy in the flags loop).
	// Promoted from Debug to Info above a slow-flush threshold so the
	// signal lands in journald during load events without noise in
	// steady state.
	const slowFlushThreshold = 500 * time.Millisecond
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
