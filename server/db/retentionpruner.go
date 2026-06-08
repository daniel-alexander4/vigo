// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

	// Reclaim space from deleted rows.
	if p.isPostgres {
		// Postgres handles vacuuming via autovacuum; no manual action needed.
	} else {
		if _, err := p.sqlDB.ExecContext(ctx, "PRAGMA incremental_vacuum(1000)"); err != nil {
			slog.Error("incremental vacuum failed", "error", err)
		}
	}

	slog.Info("retention pruner complete", "deleted_total", res.Total())
	return res
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
