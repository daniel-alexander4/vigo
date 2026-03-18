// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package push provides the push timeout reaper — a background goroutine
// that marks expired push runs and their pending targets as timed out.
package push

import (
	"context"
	"log/slog"
	"time"

	"github.com/hobnob/hobnob/server/db"
)

// Reaper periodically scans for push runs that have exceeded their
// timeout_secs and marks them (and their pending targets) as timed out.
// Runs on a 30-second ticker. Zero impact on the check-in hot path.
type Reaper struct {
	queries *db.Queries
}

// NewReaper creates a push timeout reaper.
func NewReaper(queries *db.Queries) *Reaper {
	return &Reaper{queries: queries}
}

// Start runs the reaper loop. Call in a goroutine. Stops when ctx is cancelled.
func (r *Reaper) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reap(ctx)
		}
	}
}

func (r *Reaper) reap(ctx context.Context) {
	ids, err := r.queries.TimeoutExpiredPushRuns(ctx)
	if err != nil {
		slog.Error("push reaper: listing expired runs", "error", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	for _, id := range ids {
		_ = r.queries.TimeoutPushRunTargets(ctx, id)
		_ = r.queries.TimeoutPushRun(ctx, id)
	}
	slog.Info("push reaper: timed out expired runs", "count", len(ids))
}
