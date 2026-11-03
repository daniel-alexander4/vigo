// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package task provides the task timeout reaper — a background goroutine
// that marks expired task runs and their pending targets as timed out.
package task

import (
	"context"
	"log/slog"
	"time"

	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
)

// Reaper periodically scans for task runs that have exceeded their
// timeout_secs and marks them (and their pending targets) as timed out.
// Runs on a 30-second ticker. Zero impact on the check-in hot path.
type Reaper struct {
	queries *db.Queries
}

// NewReaper creates a task timeout reaper.
func NewReaper(queries *db.Queries) *Reaper {
	return &Reaper{queries: queries}
}

// Start runs the reaper loop. Call in a goroutine. Stops when ctx is cancelled.
func (r *Reaper) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Sweep once on startup so runs that expired while the server was down
	// are reaped immediately, not up to 30s later on the first tick.
	daemon.Guard("task-reaper", func() { r.reap(ctx) })

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			daemon.Guard("task-reaper", func() { r.reap(ctx) })
		}
	}
}

func (r *Reaper) reap(ctx context.Context) {
	ids, err := r.queries.TimeoutExpiredTaskRuns(ctx)
	if err != nil {
		slog.Error("task reaper: listing expired runs", "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	for _, id := range ids {
		if err := r.queries.TimeoutTaskRunTargets(ctx, id); err != nil {
			slog.Error("task reaper: timing out run targets", "run_id", id, "error", err)
		}
		if err := r.queries.TimeoutTaskRun(ctx, id); err != nil {
			slog.Error("task reaper: timing out run", "run_id", id, "error", err)
		}
	}
	slog.Info("task reaper: timed out expired runs", "count", len(ids))
}
