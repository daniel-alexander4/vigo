// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package db provides database access and the RunStore abstraction for
// pluggable run storage backends.
package db

import (
	"context"

	"github.com/google/uuid"
)

// RunStore abstracts run data storage. Two backends are available:
//   - DBRunStore: persists to the database (default, durable)
//   - MemRunStore: in-memory ring buffer (fast, lost on restart)
//
// Toggle via tuning.run_store in server.yaml ("database" or "memory").
type RunStore interface {
	// RecordRun persists a complete convergence run — the run row, its result
	// rows, and the finished status/counts — atomically. The DB backend runs
	// it in a single transaction on the write connection: the run is never
	// visible in a half-written "running" state, and the three writes take one
	// write-lock acquisition instead of three. Results' RunID is filled in
	// from the created run; Finish.ID likewise. Returns the finished run.
	RecordRun(ctx context.Context, arg RecordRunParams) (Run, error)
	CreateRun(ctx context.Context, arg CreateRunParams) (Run, error)
	CreateRunResult(ctx context.Context, arg CreateRunResultParams) (RunResult, error)
	// BatchCreateRunResults inserts multiple results in a single round-trip.
	// Falls back to per-row inserts if the batch is empty.
	BatchCreateRunResults(ctx context.Context, args []CreateRunResultParams) error
	FinishRun(ctx context.Context, arg FinishRunParams) error
	GetRunByID(ctx context.Context, id string) (Run, error)
	ListAllRuns(ctx context.Context, limit int64) ([]Run, error)
	ListRunResults(ctx context.Context, runID string) ([]RunResult, error)
	ListRunsByEnvoy(ctx context.Context, arg ListRunsByEnvoyParams) ([]Run, error)

	// LatestRunPerEnvoy returns the most recent run for each given envoy ID.
	// DB backend uses a single optimized query; memory backend does map lookups.
	LatestRunPerEnvoy(ctx context.Context, envoyIDs []string) (map[string]*Run, error)
}

// RecordRunParams bundles everything one convergence report writes. Results,
// FileSnapshots and Finish carry a blank run id — RecordRun stamps them from
// the run it creates. Run.ID is the server-minted run id (NewRunID); left
// blank, the store mints one. Callers that publish the id before the write
// lands (the async delta path) mint it up front so the fleet index and the DB
// row agree.
type RecordRunParams struct {
	Run           CreateRunParams
	Results       []CreateRunResultParams
	FileSnapshots []InsertFileSnapshotParams // changed file resources (Rollback evidence); DB store only
	Finish        FinishRunParams
}

// NewRunID mints a server-side run id (UUIDv4 text, the same shape as the
// runs.id column default). The agent's own run_id is never used as a key.
func NewRunID() string { return uuid.New().String() }
