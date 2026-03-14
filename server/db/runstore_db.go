// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"fmt"
	"strings"
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

// BatchCreateRunResults inserts all results in a single multi-row INSERT,
// reducing N round-trips to 1. SQLite supports up to 999 bound parameters,
// so batches are chunked at 100 rows (800 params) to stay well under the limit.
func (s *DBRunStore) BatchCreateRunResults(ctx context.Context, args []CreateRunResultParams) error {
	if len(args) == 0 {
		return nil
	}
	const chunkSize = 100 // 8 columns × 100 = 800 params (SQLite limit is 999)
	for i := 0; i < len(args); i += chunkSize {
		end := i + chunkSize
		if end > len(args) {
			end = len(args)
		}
		if err := s.batchInsertRunResults(ctx, args[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *DBRunStore) batchInsertRunResults(ctx context.Context, args []CreateRunResultParams) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO run_results (run_id, module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms) VALUES ")
	params := make([]interface{}, 0, len(args)*8)
	for i, a := range args {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?,?,?,?)")
		params = append(params, a.RunID, a.ModuleName, a.ResourceType, a.ResourceName, a.ActionTaken, a.Changed, a.ErrorMessage, a.DurationMs)
	}
	_, err := s.q.db.ExecContext(ctx, sb.String(), params...)
	if err != nil {
		return fmt.Errorf("batch insert run results: %w", err)
	}
	return nil
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

func (s *DBRunStore) ListRunsByThrall(ctx context.Context, arg ListRunsByThrallParams) ([]Run, error) {
	return s.q.ListRunsByThrall(ctx, arg)
}

func (s *DBRunStore) LatestRunPerThrall(ctx context.Context, thrallIDs []string) (map[string]*Run, error) {
	// Use the optimized correlated subquery.
	rows, err := s.q.ListThrallsWithLatestRun(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Run, len(rows))
	for _, r := range rows {
		if r.LatestRunStatus.Valid {
			run := Run{
				ThrallID:       r.ID,
				Status:       r.LatestRunStatus.String,
				ChangedCount: r.LatestRunChangedCount.Int64,
			}
			result[r.ID] = &run
		}
	}
	return result, nil
}
