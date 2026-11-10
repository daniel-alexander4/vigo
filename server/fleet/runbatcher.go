// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
)

// DefaultRunBatcherMaxPending bounds the runs held between flushes. A flush
// runs every few seconds; 10k runs is several fleet-wide check-in waves of
// headroom, past which a stalled writer sheds new runs instead of growing the
// heap without bound.
const DefaultRunBatcherMaxPending = 10000

// RunBatcher accumulates completed runs from the stream delta path and
// persists each one with RunStore.RecordRun (run row + results + file
// snapshots + finish, atomically) on a ticker. Follows the same
// ticker+shutdown pattern as StateFlusher.
//
// Every entry carries its server-minted run id (Run.ID) already: the delta
// handler publishes that id to the FleetIndex before the write lands, so the
// in-memory LastRunID / recent-runs links and the DB row name the same run.
type RunBatcher struct {
	store      db.RunStore
	maxPending int
	mu         sync.Mutex
	pending    []db.RecordRunParams
	dropped    atomic.Uint64 // runs shed because pending was full (lifetime)
	reported   uint64        // dropped count already logged; guarded by mu
}

// NewRunBatcher creates a RunBatcher that writes to the given RunStore,
// holding at most DefaultRunBatcherMaxPending runs between flushes.
func NewRunBatcher(store db.RunStore) *RunBatcher {
	return NewRunBatcherWithLimit(store, DefaultRunBatcherMaxPending)
}

// NewRunBatcherWithLimit is NewRunBatcher with an explicit pending bound
// (<= 0 → DefaultRunBatcherMaxPending).
func NewRunBatcherWithLimit(store db.RunStore, maxPending int) *RunBatcher {
	if maxPending <= 0 {
		maxPending = DefaultRunBatcherMaxPending
	}
	return &RunBatcher{store: store, maxPending: maxPending}
}

// Submit queues a run for the next flush. Returns false — and counts the run
// in Dropped — when the pending batch is full: the run's history row is lost
// (the FleetIndex already holds its real-time state), but the heap stays
// bounded while the writer is stalled.
func (rb *RunBatcher) Submit(run db.RecordRunParams) bool {
	rb.mu.Lock()
	if len(rb.pending) >= rb.maxPending {
		rb.mu.Unlock()
		rb.dropped.Add(1)
		return false
	}
	rb.pending = append(rb.pending, run)
	rb.mu.Unlock()
	return true
}

// Dropped returns how many runs Submit has shed since start.
func (rb *RunBatcher) Dropped() uint64 { return rb.dropped.Load() }

// Start runs the flush loop. Blocks until ctx is cancelled, then performs
// a final flush. The offset parameter staggers the start relative to other
// flushers to avoid SQLite write lock contention.
func (rb *RunBatcher) Start(ctx context.Context, interval, offset time.Duration) {
	if offset > 0 {
		select {
		case <-time.After(offset):
		case <-ctx.Done():
			rb.Flush(context.Background())
			return
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			rb.Flush(context.Background())
			return
		case <-ticker.C:
			daemon.Guard("run-batcher", func() { rb.Flush(ctx) })
		}
	}
}

// Flush swaps the pending slice and records each run. Errors are logged
// per-run but do not re-queue (FleetIndex has the data for real-time
// queries; DB is for history only). Also logs, once per flush, any runs shed
// since the previous flush.
func (rb *RunBatcher) Flush(ctx context.Context) {
	rb.mu.Lock()
	batch := rb.pending
	rb.pending = nil
	d, prev := rb.dropped.Load(), rb.reported
	rb.reported = d
	rb.mu.Unlock()

	if d > prev {
		slog.Warn("run batcher: pending batch full, runs shed (history rows lost; fleet index unaffected)",
			"shed_since_last_flush", d-prev, "shed_total", d, "max_pending", rb.maxPending)
	}

	if len(batch) == 0 {
		return
	}

	for _, run := range batch {
		if _, err := rb.store.RecordRun(ctx, run); err != nil {
			slog.Error("run batcher: persist failed",
				"run_id", run.Run.ID,
				"envoy_id", run.Run.EnvoyID,
				"error", err,
			)
		}
	}

	slog.Debug("run batcher: flushed", "count", len(batch))
}
