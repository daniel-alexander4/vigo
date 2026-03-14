// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
	thrallID := uuid.New().String()

	run, err := s.CreateRun(ctx, CreateRunParams{
		ThrallID:      thrallID,
		Status:      "running",
		ModuleCount: 3,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.ID == "" {
		t.Fatal("expected non-empty run ID")
	}
	if run.ThrallID != thrallID {
		t.Fatalf("expected thrall %s, got %s", thrallID, run.ThrallID)
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
		ThrallID:      uuid.New().String(),
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
		ThrallID:      uuid.New().String(),
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

func TestMemRunStore_ListRunsByThrall(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()
	thrallID := uuid.New().String()

	for i := 0; i < 5; i++ {
		s.CreateRun(ctx, CreateRunParams{ThrallID: thrallID, Status: "success", ModuleCount: 1})
	}

	runs, err := s.ListRunsByThrall(ctx, ListRunsByThrallParams{ThrallID: thrallID, Limit: 3})
	if err != nil {
		t.Fatalf("ListRunsByThrall: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
}

func TestMemRunStore_ListRunsByThrall_Empty(t *testing.T) {
	s := NewMemRunStore(10)
	runs, err := s.ListRunsByThrall(context.Background(), ListRunsByThrallParams{ThrallID: uuid.New().String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListRunsByThrall: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs, got %d", len(runs))
	}
}

func TestMemRunStore_ListAllRuns(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		s.CreateRun(ctx, CreateRunParams{ThrallID: uuid.New().String(), Status: "success", ModuleCount: 1})
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
	thrallID := uuid.New().String()

	var firstRunID string
	for i := 0; i < 5; i++ {
		run, _ := s.CreateRun(ctx, CreateRunParams{ThrallID: thrallID, Status: "success", ModuleCount: 1})
		if i == 0 {
			firstRunID = run.ID
		}
	}

	// Only capacity runs should remain for this thrall.
	runs, _ := s.ListRunsByThrall(ctx, ListRunsByThrallParams{ThrallID: thrallID, Limit: 100})
	if len(runs) != capacity {
		t.Fatalf("expected %d runs after eviction, got %d", capacity, len(runs))
	}

	// First run should have been evicted.
	_, err := s.GetRunByID(ctx, firstRunID)
	if err == nil {
		t.Fatal("expected evicted run to be gone")
	}
}

func TestMemRunStore_LatestRunPerThrall(t *testing.T) {
	s := NewMemRunStore(10)
	ctx := context.Background()

	thrall1, thrall2, thrall3 := uuid.New().String(), uuid.New().String(), uuid.New().String()

	// Create runs for thrall1 and thrall2 only.
	s.CreateRun(ctx, CreateRunParams{ThrallID: thrall1, Status: "success", ModuleCount: 1})
	run1b, _ := s.CreateRun(ctx, CreateRunParams{ThrallID: thrall1, Status: "failure", ModuleCount: 2})
	run2, _ := s.CreateRun(ctx, CreateRunParams{ThrallID: thrall2, Status: "success", ModuleCount: 1})

	result, err := s.LatestRunPerThrall(ctx, []string{thrall1, thrall2, thrall3})
	if err != nil {
		t.Fatalf("LatestRunPerThrall: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result[thrall1].ID != run1b.ID {
		t.Fatalf("expected latest run %s for thrall1, got %s", run1b.ID, result[thrall1].ID)
	}
	if result[thrall2].ID != run2.ID {
		t.Fatalf("expected latest run %s for thrall2, got %s", run2.ID, result[thrall2].ID)
	}
	if _, ok := result[thrall3]; ok {
		t.Fatal("thrall3 should not have a run")
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
