// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"git.alexander4.io/vigo/server/daemon"
)

// RetentionPruner periodically deletes old rows from history tables
// (runs, task_runs, workflow_runs, compliance_history, file_snapshots)
// and runs VACUUM to reclaim disk space.
type RetentionPruner struct {
	queries    *Queries
	sqlDB      *sql.DB
	retention  time.Duration
	isPostgres bool
}

// NewRetentionPruner creates a pruner that deletes rows older than retention.
func NewRetentionPruner(queries *Queries, sqlDB *sql.DB, retention time.Duration) *RetentionPruner {
	return &RetentionPruner{
		queries:   queries,
		sqlDB:     sqlDB,
		retention: retention,
	}
}

// SetPostgres marks the pruner as running against Postgres (skips SQLite-specific PRAGMAs).
func (p *RetentionPruner) SetPostgres(v bool) { p.isPostgres = v }

// Start runs the prune loop at the given interval. Blocks until ctx is cancelled.
func (p *RetentionPruner) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once on startup after a short delay (don't block server init).
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		daemon.Guard("retention-pruner", func() { p.Prune(ctx) })
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			daemon.Guard("retention-pruner", func() { p.Prune(ctx) })
		}
	}
}

// PruneResult reports how many rows the latest prune deleted per table.
// Cascaded rows (e.g., run_results dropped via runs FK) are not counted —
// only the directly-targeted DELETEs.
type PruneResult struct {
	Deleted map[string]int64
}

// Total returns the sum of directly-deleted rows across all tables.
func (r PruneResult) Total() int64 {
	var n int64
	for _, v := range r.Deleted {
		n += v
	}
	return n
}

// retentionBatchSize bounds how many rows each prune DELETE removes per
// statement. A modest size keeps each (cascade-amplified) batch a sub-second
// transaction so the single SQLite writer is released to queued check-in
// writes between batches — see pruneBatched.
const retentionBatchSize = 2000

// Prune deletes old rows from all history tables and runs incremental VACUUM.
// Returns per-table delete counts so callers can surface progress.
func (p *RetentionPruner) Prune(ctx context.Context) PruneResult {
	cutoff := time.Now().Add(-p.retention)
	slog.Info("retention pruner running", "cutoff", cutoff.Format(time.RFC3339), "retention", p.retention)

	res := PruneResult{Deleted: make(map[string]int64)}
	// History tables compare against a string date, not time.Time.
	cutoffDate := cutoff.Format("2006-01-02")

	// One bounded batch per table. Each closure deletes up to
	// retentionBatchSize rows older than the cutoff; pruneBatched loops it
	// until the table is drained.
	steps := []struct {
		name string
		one  func(context.Context) (int64, error)
	}{
		{"runs", func(ctx context.Context) (int64, error) {
			return p.queries.PruneRunsByAge(ctx, PruneRunsByAgeParams{StartedAt: cutoff, Limit: retentionBatchSize})
		}},
		{"task_runs", func(ctx context.Context) (int64, error) {
			return p.queries.PruneTaskRunsByAge(ctx, PruneTaskRunsByAgeParams{CreatedAt: cutoff, Limit: retentionBatchSize})
		}},
		{"workflow_runs", func(ctx context.Context) (int64, error) {
			return p.queries.PruneWorkflowRunsByAge(ctx, PruneWorkflowRunsByAgeParams{CreatedAt: cutoff, Limit: retentionBatchSize})
		}},
		{"file_snapshots", func(ctx context.Context) (int64, error) {
			return p.queries.PruneFileSnapshotsByAge(ctx, PruneFileSnapshotsByAgeParams{CreatedAt: cutoff, Limit: retentionBatchSize})
		}},
		{"convergence_history", func(ctx context.Context) (int64, error) {
			return p.queries.PruneConvergenceHistoryByAge(ctx, PruneConvergenceHistoryByAgeParams{RecordedAt: cutoffDate, Limit: retentionBatchSize})
		}},
		{"compliance_history", func(ctx context.Context) (int64, error) {
			return p.queries.PruneComplianceHistoryByAge(ctx, PruneComplianceHistoryByAgeParams{SnapshotDate: cutoffDate, Limit: retentionBatchSize})
		}},
	}

	for _, s := range steps {
		res.Deleted[s.name] = p.pruneBatched(ctx, s.name, s.one)
	}

	// Deleted pages go to the freelist and are reused in place by future
	// inserts — the file holds at its working-set high-water mark. SQLite has
	// no effective online shrink under WAL (auto_vacuum can't be enabled
	// post-creation in WAL mode, and incremental_vacuum is ineffective for the
	// delete-oldest pattern), so a large freed backlog is reclaimed only by an
	// operator `vigocli db vacuum`. Postgres reclaims via autovacuum.
	if !p.isPostgres {
		p.recommendVacuumIfBloated(ctx)
	}

	slog.Info("retention pruner complete", "deleted_total", res.Total())
	return res
}

// Vacuum is recommended only when the freelist is both a large fraction of the
// file and large in absolute terms — a small freelist is healthy reuse slack,
// not worth a locking full VACUUM.
const (
	vacuumBloatPct   = 25        // freelist as a percentage of total pages
	vacuumMinReclaim = 100 << 20 // and at least this many bytes reclaimable
)

// vacuumRecommended reports whether the freelist is large enough that an
// operator VACUUM would meaningfully shrink the file.
func vacuumRecommended(freelistPages, pageCount, pageSize int64) bool {
	if freelistPages <= 0 || pageCount <= 0 {
		return false
	}
	if freelistPages*pageSize < vacuumMinReclaim {
		return false
	}
	return freelistPages*100 >= pageCount*vacuumBloatPct
}

// recommendVacuumIfBloated logs a one-line recommendation to run
// `vigocli db vacuum` when the freelist has grown large. Advisory only — the
// pruner never VACUUMs itself (exclusive lock + 2× disk is an operator call).
func (p *RetentionPruner) recommendVacuumIfBloated(ctx context.Context) {
	var freelist, pageCount, pageSize int64
	if err := p.sqlDB.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freelist); err != nil {
		return
	}
	if err := p.sqlDB.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return
	}
	if err := p.sqlDB.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return
	}
	if vacuumRecommended(freelist, pageCount, pageSize) {
		slog.Warn("database freelist is large; run `vigocli db vacuum` to reclaim disk space",
			"freelist_pages", freelist,
			"reclaimable_mb", freelist*pageSize>>20,
			"bloat_pct", freelist*100/pageCount)
	}
}

// pruneBatched runs one bounded delete batch repeatedly until a short batch
// (fewer than retentionBatchSize rows) signals the table is drained,
// returning the total deleted. Batching keeps the unbounded first-enablement
// backlog from holding the single SQLite writer for minutes: each batch is
// its own implicit transaction, so the writer connection is released to
// queued check-in (ReportResult) writes between iterations. A failed batch is
// logged and ends the table with partial progress kept; ctx cancellation
// (shutdown) stops the loop cleanly.
func (p *RetentionPruner) pruneBatched(ctx context.Context, table string, one func(context.Context) (int64, error)) int64 {
	var total int64
	for {
		if ctx.Err() != nil {
			return total
		}
		n, err := one(ctx)
		if err != nil {
			slog.Error("retention pruner failed", "table", table, "error", err)
			return total
		}
		total += n
		if n < retentionBatchSize {
			return total
		}
	}
}

// DatabaseSizeBytes returns the current database file size in bytes.
// Works with both SQLite (pragma) and Postgres (pg_database_size).
func DatabaseSizeBytes(sqlDB *sql.DB) (int64, error) {
	var size int64
	// Try Postgres first.
	err := sqlDB.QueryRow("SELECT pg_database_size(current_database())").Scan(&size)
	if err == nil {
		return size, nil
	}
	// Fall back to SQLite pragma.
	err = sqlDB.QueryRow("SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&size)
	if err != nil {
		return 0, fmt.Errorf("querying database size: %w", err)
	}
	return size, nil
}
