package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

func TestMemRunStore_CreateAndGet(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()
	nodeID := uuid.New().String()

	run, err := s.CreateRun(ctx, CreateRunParams{
		NodeID:      nodeID,
		Status:      "running",
		ModuleCount: 3,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.ID == "" {
		t.Fatal("expected non-empty run ID")
	}
	if run.NodeID != nodeID {
		t.Fatalf("expected node %s, got %s", nodeID, run.NodeID)
	}
	if run.Status != "running" {
		t.Fatalf("expected status running, got %s", run.Status)
	}

	got, err := s.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRunByID: %v", err)
	}
	if got.ID != run.ID {
		t.Fatalf("expected run %s, got %s", run.ID, got.ID)
	}
}

func TestMemRunStore_CreateRunResult(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()

	run, _ := s.CreateRun(ctx, CreateRunParams{
		NodeID:      uuid.New().String(),
		Status:      "running",
		ModuleCount: 1,
	})

	result, err := s.CreateRunResult(ctx, CreateRunResultParams{
		RunID:        run.ID,
		ModuleName:   "nginx",
		ResourceType: "package",
		ResourceName: "nginx",
		ActionTaken:  "created",
		Changed:      1,
		ErrorMessage: sql.NullString{},
		DurationMs:   42,
	})
	if err != nil {
		t.Fatalf("CreateRunResult: %v", err)
	}
	if result.ModuleName != "nginx" {
		t.Fatalf("expected module nginx, got %s", result.ModuleName)
	}

	results, err := s.ListRunResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestMemRunStore_FinishRun(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()

	run, _ := s.CreateRun(ctx, CreateRunParams{
		NodeID:      uuid.New().String(),
		Status:      "running",
		ModuleCount: 2,
	})

	err := s.FinishRun(ctx, FinishRunParams{
		ID:           run.ID,
		Status:       "success",
		ChangedCount: 1,
		FailedCount:  0,
		SkippedCount: 1,
	})
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	got, _ := s.GetRunByID(ctx, run.ID)
	if got.Status != "success" {
		t.Fatalf("expected status success, got %s", got.Status)
	}
	if got.ChangedCount != 1 {
		t.Fatalf("expected changed 1, got %d", got.ChangedCount)
	}
	if !got.FinishedAt.Valid {
		t.Fatal("expected FinishedAt to be set")
	}
}

func TestMemRunStore_FinishRun_NotFound(t *testing.T) {
	s := NewMemRunStore(10)
	err := s.FinishRun(context.Background(), FinishRunParams{ID: uuid.New().String()})
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestMemRunStore_ListRunsByNode(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()
	nodeID := uuid.New().String()

	for i := 0; i < 5; i++ {
		s.CreateRun(ctx, CreateRunParams{NodeID: nodeID, Status: "success", ModuleCount: 1})
	}

	runs, err := s.ListRunsByNode(ctx, ListRunsByNodeParams{NodeID: nodeID, Limit: 3})
	if err != nil {
		t.Fatalf("ListRunsByNode: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
}

func TestMemRunStore_ListRunsByNode_Empty(t *testing.T) {
	s := NewMemRunStore(10)
	runs, err := s.ListRunsByNode(context.Background(), ListRunsByNodeParams{NodeID: uuid.New().String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListRunsByNode: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs, got %d", len(runs))
	}
}

func TestMemRunStore_ListAllRuns(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		s.CreateRun(ctx, CreateRunParams{NodeID: uuid.New().String(), Status: "success", ModuleCount: 1})
	}

	runs, err := s.ListAllRuns(ctx, 3)
	if err != nil {
		t.Fatalf("ListAllRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
}

func TestMemRunStore_Eviction(t *testing.T) {
	capacity := 3
	s := NewMemRunStore(capacity)
	ctx := context.Background()
	nodeID := uuid.New().String()

	var firstRunID string
	for i := 0; i < 5; i++ {
		run, _ := s.CreateRun(ctx, CreateRunParams{NodeID: nodeID, Status: "success", ModuleCount: 1})
		if i == 0 {
			firstRunID = run.ID
		}
	}

	// Only capacity runs should remain for this node.
	runs, _ := s.ListRunsByNode(ctx, ListRunsByNodeParams{NodeID: nodeID, Limit: 100})
	if len(runs) != capacity {
		t.Fatalf("expected %d runs after eviction, got %d", capacity, len(runs))
	}

	// First run should have been evicted.
	_, err := s.GetRunByID(ctx, firstRunID)
	if err == nil {
		t.Fatal("expected evicted run to be gone")
	}
}

func TestMemRunStore_LatestRunPerNode(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()

	node1, node2, node3 := uuid.New().String(), uuid.New().String(), uuid.New().String()

	// Create runs for node1 and node2 only.
	s.CreateRun(ctx, CreateRunParams{NodeID: node1, Status: "success", ModuleCount: 1})
	run1b, _ := s.CreateRun(ctx, CreateRunParams{NodeID: node1, Status: "failure", ModuleCount: 2})
	run2, _ := s.CreateRun(ctx, CreateRunParams{NodeID: node2, Status: "success", ModuleCount: 1})

	result, err := s.LatestRunPerNode(ctx, []string{node1, node2, node3})
	if err != nil {
		t.Fatalf("LatestRunPerNode: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result[node1].ID != run1b.ID {
		t.Fatalf("expected latest run %s for node1, got %s", run1b.ID, result[node1].ID)
	}
	if result[node2].ID != run2.ID {
		t.Fatalf("expected latest run %s for node2, got %s", run2.ID, result[node2].ID)
	}
	if _, ok := result[node3]; ok {
		t.Fatal("node3 should not have a run")
	}
}

func TestMemRunStore_DefaultCapacity(t *testing.T) {
	s := NewMemRunStore(0)
	if s.capacity != 20 {
		t.Fatalf("expected default capacity 20, got %d", s.capacity)
	}

	s = NewMemRunStore(-1)
	if s.capacity != 20 {
		t.Fatalf("expected default capacity 20, got %d", s.capacity)
	}
}

func TestMemRunStore_GetRunByID_NotFound(t *testing.T) {
	s := NewMemRunStore(10)
	_, err := s.GetRunByID(context.Background(), uuid.New().String())
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestMemRunStore_ListRunResults_NotFound(t *testing.T) {
	s := NewMemRunStore(10)
	_, err := s.ListRunResults(context.Background(), uuid.New().String())
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}
