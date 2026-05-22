// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

const numShards = 64

// MemRunStore stores runs in memory using per-envoy ring buffers, sharded
// across 64 independent locks to reduce contention. Faster than the DB
// backend but data is lost on server restart.
// Enable via tuning.run_store: "memory" in server.yaml.
type MemRunStore struct {
	shards   [numShards]runShard
	capacity int // max runs per envoy
}

type runShard struct {
	mu       sync.RWMutex
	byID     map[string]*memRun          // run UUID string → run + results
	byEnvoy map[string]*envoyRunBuffer // envoy UUID string → ring of run IDs
}

type memRun struct {
	run     Run
	results []RunResult
}

// envoyRunBuffer is a bounded ring buffer of run IDs per envoy.
type envoyRunBuffer struct {
	runs []string // newest first
}

// NewMemRunStore creates an in-memory run store keeping at most capacity
// runs per envoy.
func NewMemRunStore(capacity int) *MemRunStore {
	if capacity < 1 {
		capacity = 20
	}
	s := &MemRunStore{capacity: capacity}
	for i := range s.shards {
		s.shards[i] = runShard{
			byID:     make(map[string]*memRun),
			byEnvoy: make(map[string]*envoyRunBuffer),
		}
	}
	return s
}

// shard returns the shard for a given envoy ID.
func (s *MemRunStore) shard(envoyID string) *runShard {
	h := fnv.New32a()
	h.Write([]byte(envoyID))
	return &s.shards[h.Sum32()%numShards]
}

// shardByRunID finds which shard contains a run ID. Since run IDs are UUIDs
// and we index by envoy, we need to check all shards for by-ID lookups.
// For hot-path operations (CreateRunResult, FinishRun) we store the envoy ID
// in memRun so we can shard directly.
func (s *MemRunStore) findRunShard(runID string) (*runShard, *memRun) {
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		mr, ok := sh.byID[runID]
		sh.mu.RUnlock()
		if ok {
			return sh, mr
		}
	}
	return nil, nil
}

func (s *MemRunStore) CreateRun(_ context.Context, arg CreateRunParams) (Run, error) {
	trigger := arg.Trigger
	if trigger == "" {
		trigger = "scheduled"
	}
	run := Run{
		ID:          uuid.New().String(),
		EnvoyID:    arg.EnvoyID,
		StartedAt:   time.Now(),
		Status:      arg.Status,
		Trigger:     trigger,
		ModuleCount: arg.ModuleCount,
	}

	sh := s.shard(arg.EnvoyID)
	sh.mu.Lock()
	sh.byID[run.ID] = &memRun{run: run}
	buf := sh.byEnvoy[arg.EnvoyID]
	if buf == nil {
		buf = &envoyRunBuffer{}
		sh.byEnvoy[arg.EnvoyID] = buf
	}
	// Prepend (newest first).
	buf.runs = append([]string{run.ID}, buf.runs...)
	// Evict oldest if over capacity.
	if len(buf.runs) > s.capacity {
		evictID := buf.runs[len(buf.runs)-1]
		buf.runs = buf.runs[:s.capacity]
		delete(sh.byID, evictID)
	}
	sh.mu.Unlock()

	return run, nil
}

func (s *MemRunStore) CreateRunResult(_ context.Context, arg CreateRunResultParams) (RunResult, error) {
	result := RunResult{
		ID:           uuid.New().String(),
		RunID:        arg.RunID,
		ModuleName:   arg.ModuleName,
		ResourceType: arg.ResourceType,
		ResourceName: arg.ResourceName,
		ActionTaken:  arg.ActionTaken,
		Changed:      arg.Changed,
		ErrorMessage: arg.ErrorMessage,
		DurationMs:   arg.DurationMs,
		CreatedAt:    time.Now(),
	}

	sh, _ := s.findRunShard(arg.RunID)
	if sh == nil {
		return result, nil
	}
	sh.mu.Lock()
	if mr, ok := sh.byID[arg.RunID]; ok {
		mr.results = append(mr.results, result)
	}
	sh.mu.Unlock()

	return result, nil
}

func (s *MemRunStore) BatchCreateRunResults(_ context.Context, args []CreateRunResultParams) error {
	if len(args) == 0 {
		return nil
	}
	// Group by runID to minimize shard lookups.
	byRun := make(map[string][]RunResult, 1) // typically all same run
	now := time.Now()
	for _, a := range args {
		byRun[a.RunID] = append(byRun[a.RunID], RunResult{
			ID:           uuid.New().String(),
			RunID:        a.RunID,
			ModuleName:   a.ModuleName,
			ResourceType: a.ResourceType,
			ResourceName: a.ResourceName,
			ActionTaken:  a.ActionTaken,
			Changed:      a.Changed,
			ErrorMessage: a.ErrorMessage,
			DurationMs:   a.DurationMs,
			CreatedAt:    now,
		})
	}
	for runID, results := range byRun {
		sh, _ := s.findRunShard(runID)
		if sh == nil {
			continue
		}
		sh.mu.Lock()
		if mr, ok := sh.byID[runID]; ok {
			mr.results = append(mr.results, results...)
		}
		sh.mu.Unlock()
	}
	return nil
}

func (s *MemRunStore) FinishRun(_ context.Context, arg FinishRunParams) error {
	sh, _ := s.findRunShard(arg.ID)
	if sh == nil {
		return fmt.Errorf("run %s not found", arg.ID)
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()

	mr, ok := sh.byID[arg.ID]
	if !ok {
		return fmt.Errorf("run %s not found", arg.ID)
	}
	mr.run.Status = arg.Status
	mr.run.ChangedCount = arg.ChangedCount
	mr.run.FailedCount = arg.FailedCount
	mr.run.SkippedCount = arg.SkippedCount
	mr.run.FinishedAt = sql.NullTime{Time: time.Now(), Valid: true}
	return nil
}

func (s *MemRunStore) GetRunByID(_ context.Context, id string) (Run, error) {
	sh, mr := s.findRunShard(id)
	if sh == nil || mr == nil {
		return Run{}, fmt.Errorf("run %s not found", id)
	}
	sh.mu.RLock()
	run := mr.run
	sh.mu.RUnlock()
	return run, nil
}

func (s *MemRunStore) ListAllRuns(_ context.Context, limit int64) ([]Run, error) {
	var runs []Run
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for _, mr := range sh.byID {
			runs = append(runs, mr.run)
		}
		sh.mu.RUnlock()
	}

	// Sort by started_at DESC.
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})

	if int64(len(runs)) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (s *MemRunStore) ListRunResults(_ context.Context, runID string) ([]RunResult, error) {
	sh, mr := s.findRunShard(runID)
	if sh == nil || mr == nil {
		return nil, fmt.Errorf("run %s not found", runID)
	}

	sh.mu.RLock()
	// Return a copy sorted by created_at ASC.
	results := make([]RunResult, len(mr.results))
	copy(results, mr.results)
	sh.mu.RUnlock()

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})
	return results, nil
}

func (s *MemRunStore) ListRunsByEnvoy(_ context.Context, arg ListRunsByEnvoyParams) ([]Run, error) {
	sh := s.shard(arg.EnvoyID)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	buf, ok := sh.byEnvoy[arg.EnvoyID]
	if !ok {
		return []Run{}, nil
	}

	limit := int(arg.Limit)
	if limit > len(buf.runs) {
		limit = len(buf.runs)
	}

	runs := make([]Run, 0, limit)
	for _, id := range buf.runs[:limit] {
		if mr, ok := sh.byID[id]; ok {
			runs = append(runs, mr.run)
		}
	}
	return runs, nil
}

func (s *MemRunStore) LatestRunPerEnvoy(_ context.Context, envoyIDs []string) (map[string]*Run, error) {
	result := make(map[string]*Run, len(envoyIDs))
	for _, envoyID := range envoyIDs {
		sh := s.shard(envoyID)
		sh.mu.RLock()
		buf, ok := sh.byEnvoy[envoyID]
		if !ok || len(buf.runs) == 0 {
			sh.mu.RUnlock()
			continue
		}
		if mr, ok := sh.byID[buf.runs[0]]; ok {
			run := mr.run
			result[envoyID] = &run
		}
		sh.mu.RUnlock()
	}
	return result, nil
}
