// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/metrics"
)

// StateFlusher reads dirty envoys from the FleetIndex and batch-writes their
// state changes (last_seen, force_push, pending_rotated_secrets) to SQLite
// periodically. It replaces the LastSeenBatcher with a more comprehensive
// flush that covers all mutable envoy fields.
type StateFlusher struct {
	fleet   *FleetIndex
	queries *db.Queries
}

// NewStateFlusher creates a flusher that writes dirty state from the FleetIndex
// to SQLite.
func NewStateFlusher(fleet *FleetIndex, queries *db.Queries) *StateFlusher {
	return &StateFlusher{
		fleet:   fleet,
		queries: queries,
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
	dirty := sf.fleet.DrainDirty()
	if len(dirty) == 0 {
		return
	}

	start := time.Now()
	metrics.FleetIndexDirtyCount.Set(float64(len(dirty)))
	defer func() {
		metrics.FleetIndexFlushDuration.Observe(time.Since(start).Seconds())
	}()

	// Collect IDs for batch last_seen update.
	var lastSeenIDs []string
	for id := range dirty {
		lastSeenIDs = append(lastSeenIDs, id)
	}

	// Batch update last_seen in chunks of 500.
	const chunkSize = 500
	var lastSeenFailed bool
	for i := 0; i < len(lastSeenIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(lastSeenIDs) {
			end = len(lastSeenIDs)
		}
		if err := sf.queries.BatchUpdateLastSeen(ctx, lastSeenIDs[i:end]); err != nil {
			slog.Error("state flusher: batch last_seen failed", "count", end-i, "error", err)
			lastSeenFailed = true
		}
	}

	// Handle per-envoy flag updates.
	for id, ts := range dirty {
		// Clear force_push if it was consumed (no longer set in index).
		if !ts.ForcePush {
			if err := sf.queries.ClearForcePush(ctx, id); err != nil {
				slog.Error("state flusher: clear force_push failed", "envoy_id", id, "error", err)
			}
		}

		// Clear rotated secrets if they were consumed.
		if len(ts.PendingRotatedSecrets) == 0 {
			if err := sf.queries.ClearRotatedSecrets(ctx, id); err != nil {
				slog.Error("state flusher: clear rotated secrets failed", "envoy_id", id, "error", err)
			}
		}

		// Persist tags.
		if err := sf.queries.UpdateEnvoyTags(ctx, db.UpdateEnvoyTagsParams{
			Tags: strings.Join(ts.Tags, ","),
			ID:   id,
		}); err != nil {
			slog.Error("state flusher: update tags failed", "envoy_id", id, "error", err)
		}

		// Persist maintenance window.
		var maint sql.NullTime
		if !ts.MaintenanceUntil.IsZero() {
			maint = sql.NullTime{Time: ts.MaintenanceUntil, Valid: true}
		}
		if err := sf.queries.UpdateEnvoyMaintenance(ctx, db.UpdateEnvoyMaintenanceParams{
			MaintenanceUntil: maint,
			ID:               id,
		}); err != nil {
			slog.Error("state flusher: update maintenance failed", "envoy_id", id, "error", err)
		}
	}

	// Re-mark failed envoys as dirty for next flush.
	if lastSeenFailed {
		for _, id := range lastSeenIDs {
			sf.fleet.MarkDirty(id)
		}
	}

	slog.Debug("state flusher: flushed", "count", len(dirty))
}
