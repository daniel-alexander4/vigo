// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DBRunStore delegates run storage to the database via Queries.
// This is the default backend — runs persist across restarts.
type DBRunStore struct {
	q *Queries
	// writeDB is the single-writer pool, needed to open the RecordRun
	// transaction. Same handle the SplitDB writer pool / Postgres pool uses.
	writeDB *sql.DB
}

// NewDBRunStore wraps an existing Queries for run storage. writeDB is the
// write-side connection pool used to open the RecordRun transaction.
func NewDBRunStore(q *Queries, writeDB *sql.DB) *DBRunStore {
	return &DBRunStore{q: q, writeDB: writeDB}
}

// sqlExecer is the subset of DBTX / *sql.Tx that batchInsertRunResults needs,
// so the same chunked insert serves both the autocommit path (BatchCreateRunResults)
// and the transactional path (RecordRun).
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// RecordRun writes a full convergence run atomically in one transaction on the
// write connection: CreateRun → batch-insert results → FinishRun. Either the
// whole run lands or none of it does, so a mid-write failure can't leave an
// orphaned "running" row, and the three statements take a single write-lock
// acquisition.
func (s *DBRunStore) RecordRun(ctx context.Context, arg RecordRunParams) (Run, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin run tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	q := s.q.WithTx(tx)
	if arg.Run.ID == "" {
		arg.Run.ID = NewRunID()
	}
	run, err := q.CreateRun(ctx, arg.Run)
	if err != nil {
		return Run{}, fmt.Errorf("create run: %w", err)
	}
	for i := range arg.Results {
		arg.Results[i].RunID = run.ID
	}
	if err := batchInsertRunResults(ctx, tx, arg.Results); err != nil {
		return Run{}, err
	}
	for _, fs := range arg.FileSnapshots {
		fs.RunID = sql.NullString{String: run.ID, Valid: true}
		if err := q.InsertFileSnapshot(ctx, fs); err != nil {
			return Run{}, fmt.Errorf("insert file snapshot %s: %w", fs.FilePath, err)
		}
	}
	arg.Finish.ID = run.ID
	if err := q.FinishRun(ctx, arg.Finish); err != nil {
		return Run{}, fmt.Errorf("finish run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit run: %w", err)
	}

	// Reflect the finished state so the caller (fleet index, metrics) sees the
	// committed run without a re-read.
	run.Status = arg.Finish.Status
	run.ChangedCount = arg.Finish.ChangedCount
	run.FailedCount = arg.Finish.FailedCount
	run.SkippedCount = arg.Finish.SkippedCount
	return run, nil
}

func (s *DBRunStore) CreateRun(ctx context.Context, arg CreateRunParams) (Run, error) {
	if arg.ID == "" {
		arg.ID = NewRunID()
	}
	return s.q.CreateRun(ctx, arg)
}

func (s *DBRunStore) CreateRunResult(ctx context.Context, arg CreateRunResultParams) (RunResult, error) {
	return s.q.CreateRunResult(ctx, arg)
}

// BatchCreateRunResults inserts all results in a single multi-row INSERT,
// reducing N round-trips to 1. SQLite supports up to 999 bound parameters,
// so batches are chunked at 100 rows (800 params) to stay well under the limit.
func (s *DBRunStore) BatchCreateRunResults(ctx context.Context, args []CreateRunResultParams) error {
	return batchInsertRunResults(ctx, s.q.db, args)
}

// batchInsertRunResults inserts results via exec (the autocommit DBTX or a
// transaction) in chunks of 100 rows. SQLite supports up to 999 bound
// parameters, so 8 columns × 100 = 800 stays well under the limit.
func batchInsertRunResults(ctx context.Context, exec sqlExecer, args []CreateRunResultParams) error {
	if len(args) == 0 {
		return nil
	}
	const chunkSize = 100
	for i := 0; i < len(args); i += chunkSize {
		end := i + chunkSize
		if end > len(args) {
			end = len(args)
		}
		if err := batchInsertRunResultsChunk(ctx, exec, args[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func batchInsertRunResultsChunk(ctx context.Context, exec sqlExecer, args []CreateRunResultParams) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO run_results (run_id, configcrate_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms) VALUES ")
	params := make([]interface{}, 0, len(args)*8)
	for i, a := range args {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?,?,?,?)")
		params = append(params, a.RunID, a.ConfigcrateName, a.ResourceType, a.ResourceName, a.ActionTaken, a.Changed, a.ErrorMessage, a.DurationMs)
	}
	if _, err := exec.ExecContext(ctx, sb.String(), params...); err != nil {
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

func (s *DBRunStore) ListRunsByEnvoy(ctx context.Context, arg ListRunsByEnvoyParams) ([]Run, error) {
	return s.q.ListRunsByEnvoy(ctx, arg)
}

func (s *DBRunStore) LatestRunPerEnvoy(ctx context.Context, envoyIDs []string) (map[string]*Run, error) {
	// Use the optimized correlated subquery.
	rows, err := s.q.ListEnvoysWithLatestRun(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Run, len(rows))
	for _, r := range rows {
		if r.LatestRunStatus.Valid {
			run := Run{
				EnvoyID:       r.ID,
				Status:        r.LatestRunStatus.String,
				ChangedCount:  r.LatestRunChangedCount.Int64,
				FailedCount:   r.LatestRunFailedCount.Int64,
				ResourceCount: r.LatestRunResourceCount.Int64,
			}
			result[r.ID] = &run
		}
	}
	return result, nil
}
