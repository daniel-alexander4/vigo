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
	CreateRun(ctx context.Context, arg CreateRunParams) (Run, error)
	CreateRunResult(ctx context.Context, arg CreateRunResultParams) (RunResult, error)
	FinishRun(ctx context.Context, arg FinishRunParams) error
	GetRunByID(ctx context.Context, id uuid.UUID) (Run, error)
	ListAllRuns(ctx context.Context, limit int32) ([]Run, error)
	ListRunResults(ctx context.Context, runID uuid.UUID) ([]RunResult, error)
	ListRunsByNode(ctx context.Context, arg ListRunsByNodeParams) ([]Run, error)

	// LatestRunPerNode returns the most recent run for each given node ID.
	// DB backend uses a single optimized query; memory backend does map lookups.
	LatestRunPerNode(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]*Run, error)
}
