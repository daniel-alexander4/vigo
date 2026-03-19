// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
)

// RunBatchResultParam holds per-resource result fields for deferred DB writes.
type RunBatchResultParam struct {
	ModuleName   string
	ResourceType string
	ResourceName string
	ActionTaken  string
	Changed      bool
	ErrorMessage string
	DurationMs   int32
}

// RunBatchEntry holds a single completed run to be persisted asynchronously.
type RunBatchEntry struct {
	RunID        string
	EnvoyID     string
	Hostname     string
	Status       string
	ModuleCount  int64
	ChangedCount int64
	FailedCount  int64
	SkippedCount int64
	DBResults    []RunBatchResultParam
}

// RunBatcher accumulates run results and flushes them to the database
// periodically. Follows the same ticker+shutdown pattern as StateFlusher.
type RunBatcher struct {
	store   db.RunStore
	mu      sync.Mutex
	pending []RunBatchEntry
}

// NewRunBatcher creates a RunBatcher that writes to the given RunStore.
func NewRunBatcher(store db.RunStore) *RunBatcher {
	return &RunBatcher{
		store: store,
	}
}

// Submit adds a run entry to the pending batch.
func (rb *RunBatcher) Submit(entry RunBatchEntry) {
	rb.mu.Lock()
	rb.pending = append(rb.pending, entry)
	rb.mu.Unlock()
}

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
			rb.Flush(ctx)
		}
	}
}

// Flush swaps the pending slice and writes each entry to the database.
// Errors are logged per-entry but do not re-queue (FleetIndex has the
// data for real-time queries; DB is for history only).
func (rb *RunBatcher) Flush(ctx context.Context) {
	rb.mu.Lock()
	batch := rb.pending
	rb.pending = nil
	rb.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	for _, entry := range batch {
		if err := rb.persistEntry(ctx, entry); err != nil {
			slog.Error("run batcher: persist failed",
				"run_id", entry.RunID,
				"envoy_id", entry.EnvoyID,
				"error", err,
			)
		}
	}

	slog.Debug("run batcher: flushed", "count", len(batch))
}

// persistEntry writes a single run and its results to the database.
func (rb *RunBatcher) persistEntry(ctx context.Context, entry RunBatchEntry) error {
	run, err := rb.store.CreateRun(ctx, db.CreateRunParams{
		EnvoyID:    entry.EnvoyID,
		Status:      "running",
		ModuleCount: entry.ModuleCount,
	})
	if err != nil {
		return err
	}

	if len(entry.DBResults) > 0 {
		batch := make([]db.CreateRunResultParams, 0, len(entry.DBResults))
		for _, r := range entry.DBResults {
			errMsg := sql.NullString{}
			if r.ErrorMessage != "" {
				errMsg = sql.NullString{String: r.ErrorMessage, Valid: true}
			}
			var ch int64
			if r.Changed {
				ch = 1
			}
			batch = append(batch, db.CreateRunResultParams{
				RunID:        run.ID,
				ModuleName:   r.ModuleName,
				ResourceType: r.ResourceType,
				ResourceName: r.ResourceName,
				ActionTaken:  r.ActionTaken,
				Changed:      ch,
				ErrorMessage: errMsg,
				DurationMs:   int64(r.DurationMs),
			})
		}
		if err := rb.store.BatchCreateRunResults(ctx, batch); err != nil {
			return err
		}
	}

	return rb.store.FinishRun(ctx, db.FinishRunParams{
		ID:           run.ID,
		Status:       entry.Status,
		ChangedCount: entry.ChangedCount,
		FailedCount:  entry.FailedCount,
		SkippedCount: entry.SkippedCount,
	})
}
