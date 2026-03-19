// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package db provides database access and the RunStore abstraction for
// pluggable run storage backends.
package db

import (
	"context"
)

// RunStore abstracts run data storage. Two backends are available:
//   - DBRunStore: persists to the database (default, durable)
//   - MemRunStore: in-memory ring buffer (fast, lost on restart)
//
// Toggle via tuning.run_store in server.yaml ("database" or "memory").
type RunStore interface {
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
