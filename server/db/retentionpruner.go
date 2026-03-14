// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
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
		p.Prune(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Prune(ctx)
		}
	}
}

// Prune deletes old rows from all history tables and runs incremental VACUUM.
func (p *RetentionPruner) Prune(ctx context.Context) {
	cutoff := time.Now().Add(-p.retention)
	slog.Info("retention pruner running", "cutoff", cutoff.Format(time.RFC3339), "retention", p.retention)

	tables := []struct {
		name  string
		prune func(context.Context, time.Time) error
	}{
		{"runs", p.queries.PruneRunsByAge},
		{"task_runs", p.queries.PruneTaskRunsByAge},
		{"workflow_runs", p.queries.PruneWorkflowRunsByAge},
		{"file_snapshots", p.queries.PruneFileSnapshotsByAge},
	}

	for _, t := range tables {
		if err := t.prune(ctx, cutoff); err != nil {
			slog.Error("retention pruner failed", "table", t.name, "error", err)
		}
	}

	// Compliance history uses a string date, not time.Time.
	cutoffDate := cutoff.Format("2006-01-02")
	if err := p.queries.PruneComplianceHistoryByAge(ctx, cutoffDate); err != nil {
		slog.Error("retention pruner failed", "table", "compliance_history", "error", err)
	}

	// Reclaim space from deleted rows.
	if p.isPostgres {
		// Postgres handles vacuuming via autovacuum; no manual action needed.
	} else {
		if _, err := p.sqlDB.ExecContext(ctx, "PRAGMA incremental_vacuum(1000)"); err != nil {
			slog.Error("incremental vacuum failed", "error", err)
		}
	}

	slog.Info("retention pruner complete")
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
