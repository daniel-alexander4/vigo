package db

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// MemRunStore stores runs in memory using per-node ring buffers.
// Faster than the DB backend but data is lost on server restart.
// Enable via tuning.run_store: "memory" in server.yaml.
type MemRunStore struct {
	mu       sync.RWMutex
	byID     map[uuid.UUID]*memRun        // run UUID → run + results
	byNode   map[uuid.UUID]*nodeRunBuffer // node UUID → ring of run UUIDs
	capacity int                          // max runs per node
}

type memRun struct {
	run     Run
	results []RunResult
}

// nodeRunBuffer is a bounded ring buffer of run UUIDs per node.
type nodeRunBuffer struct {
	runs []uuid.UUID // newest first
}

// NewMemRunStore creates an in-memory run store keeping at most capacity
// runs per node.
func NewMemRunStore(capacity int) *MemRunStore {
	if capacity < 1 {
		capacity = 20
	}
	return &MemRunStore{
		byID:     make(map[uuid.UUID]*memRun),
		byNode:   make(map[uuid.UUID]*nodeRunBuffer),
		capacity: capacity,
	}
}

func (s *MemRunStore) CreateRun(_ context.Context, arg CreateRunParams) (Run, error) {
	run := Run{
		ID:          uuid.New(),
		NodeID:      arg.NodeID,
		StartedAt:   time.Now(),
		Status:      arg.Status,
		ModuleCount: arg.ModuleCount,
	}

	s.mu.Lock()
	s.byID[run.ID] = &memRun{run: run}
	buf := s.byNode[arg.NodeID]
	if buf == nil {
		buf = &nodeRunBuffer{}
		s.byNode[arg.NodeID] = buf
	}
	// Prepend (newest first).
	buf.runs = append([]uuid.UUID{run.ID}, buf.runs...)
	// Evict oldest if over capacity.
	if len(buf.runs) > s.capacity {
		evictID := buf.runs[len(buf.runs)-1]
		buf.runs = buf.runs[:s.capacity]
		delete(s.byID, evictID)
	}
	s.mu.Unlock()

	return run, nil
}

func (s *MemRunStore) CreateRunResult(_ context.Context, arg CreateRunResultParams) (RunResult, error) {
	result := RunResult{
		ID:           uuid.New(),
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

	s.mu.Lock()
	if mr, ok := s.byID[arg.RunID]; ok {
		mr.results = append(mr.results, result)
	}
	s.mu.Unlock()

	return result, nil
}

func (s *MemRunStore) FinishRun(_ context.Context, arg FinishRunParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mr, ok := s.byID[arg.ID]
	if !ok {
		return fmt.Errorf("run %s not found", arg.ID)
	}
	mr.run.Status = arg.Status
	mr.run.ChangedCount = arg.ChangedCount
	mr.run.FailedCount = arg.FailedCount
	mr.run.SkippedCount = arg.SkippedCount
	mr.run.FinishedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return nil
}

func (s *MemRunStore) GetRunByID(_ context.Context, id uuid.UUID) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mr, ok := s.byID[id]
	if !ok {
		return Run{}, fmt.Errorf("run %s not found", id)
	}
	return mr.run, nil
}

func (s *MemRunStore) ListAllRuns(_ context.Context, limit int32) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	runs := make([]Run, 0, len(s.byID))
	for _, mr := range s.byID {
		runs = append(runs, mr.run)
	}

	// Sort by started_at DESC.
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})

	if int32(len(runs)) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (s *MemRunStore) ListRunResults(_ context.Context, runID uuid.UUID) ([]RunResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mr, ok := s.byID[runID]
	if !ok {
		return nil, fmt.Errorf("run %s not found", runID)
	}

	// Return a copy sorted by created_at ASC.
	results := make([]RunResult, len(mr.results))
	copy(results, mr.results)
	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})
	return results, nil
}

func (s *MemRunStore) ListRunsByNode(_ context.Context, arg ListRunsByNodeParams) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	buf, ok := s.byNode[arg.NodeID]
	if !ok {
		return []Run{}, nil
	}

	limit := int(arg.Limit)
	if limit > len(buf.runs) {
		limit = len(buf.runs)
	}

	runs := make([]Run, 0, limit)
	for _, id := range buf.runs[:limit] {
		if mr, ok := s.byID[id]; ok {
			runs = append(runs, mr.run)
		}
	}
	return runs, nil
}

func (s *MemRunStore) LatestRunPerNode(_ context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[uuid.UUID]*Run, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		buf, ok := s.byNode[nodeID]
		if !ok || len(buf.runs) == 0 {
			continue
		}
		if mr, ok := s.byID[buf.runs[0]]; ok {
			run := mr.run
			result[nodeID] = &run
		}
	}
	return result, nil
}
