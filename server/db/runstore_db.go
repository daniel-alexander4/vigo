package db

import (
	"context"
)

// DBRunStore delegates run storage to the database via Queries.
// This is the default backend — runs persist across restarts.
type DBRunStore struct {
	q *Queries
}

// NewDBRunStore wraps an existing Queries for run storage.
func NewDBRunStore(q *Queries) *DBRunStore {
	return &DBRunStore{q: q}
}

func (s *DBRunStore) CreateRun(ctx context.Context, arg CreateRunParams) (Run, error) {
	return s.q.CreateRun(ctx, arg)
}

func (s *DBRunStore) CreateRunResult(ctx context.Context, arg CreateRunResultParams) (RunResult, error) {
	return s.q.CreateRunResult(ctx, arg)
}

func (s *DBRunStore) FinishRun(ctx context.Context, arg FinishRunParams) error {
	return s.q.FinishRun(ctx, arg)
}

func (s *DBRunStore) GetRunByID(ctx context.Context, id string) (Run, error) {
	return s.q.GetRunByID(ctx, id)
}

func (s *DBRunStore) ListAllRuns(ctx context.Context, limit int64) ([]Run, error) {
	return s.q.ListAllRuns(ctx, limit)
}

func (s *DBRunStore) ListRunResults(ctx context.Context, runID string) ([]RunResult, error) {
	return s.q.ListRunResults(ctx, runID)
}

func (s *DBRunStore) ListRunsByNode(ctx context.Context, arg ListRunsByNodeParams) ([]Run, error) {
	return s.q.ListRunsByNode(ctx, arg)
}

func (s *DBRunStore) LatestRunPerNode(ctx context.Context, nodeIDs []string) (map[string]*Run, error) {
	// Use the optimized correlated subquery.
	rows, err := s.q.ListNodesWithLatestRun(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Run, len(rows))
	for _, r := range rows {
		if r.LatestRunStatus.Valid {
			run := Run{
				NodeID:       r.ID,
				Status:       r.LatestRunStatus.String,
				ChangedCount: r.LatestRunChangedCount.Int64,
			}
			result[r.ID] = &run
		}
	}
	return result, nil
}
