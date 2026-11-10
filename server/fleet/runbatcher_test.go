// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"sync"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
)

func batchRun(envoyID, status string, resources int64) db.RecordRunParams {
	return db.RecordRunParams{
		Run:    db.CreateRunParams{ID: db.NewRunID(), EnvoyID: envoyID, Status: "running", ResourceCount: resources},
		Finish: db.FinishRunParams{Status: status},
	}
}

func TestRunBatcherSubmitAndFlush(t *testing.T) {
	store := db.NewMemRunStore(100)
	rb := NewRunBatcher(store)

	var ids []string
	for i := range 3 {
		r := batchRun("envoy-1", "success", 2)
		r.Finish.ChangedCount = 1
		r.Finish.SkippedCount = 1
		r.Results = []db.CreateRunResultParams{
			{ConfigcrateName: "nginx", ResourceType: "package", ResourceName: "nginx", ActionTaken: "none"},
			{ConfigcrateName: "nginx", ResourceType: "file", ResourceName: "/etc/nginx.conf", ActionTaken: "updated", Changed: 1, DurationMs: int64(10 * (i + 1))},
		}
		ids = append(ids, r.Run.ID)
		if !rb.Submit(r) {
			t.Fatal("Submit refused under the limit")
		}
	}

	rb.Flush(context.Background())

	for _, id := range ids {
		// The persisted row carries the id minted before Submit — the id the
		// delta handler already published to the FleetIndex.
		run, err := store.GetRunByID(context.Background(), id)
		if err != nil {
			t.Fatalf("run %s not persisted under its pre-minted id: %v", id, err)
		}
		if run.Status != "success" || run.ChangedCount != 1 {
			t.Errorf("run %s: status=%q changed=%d, want success/1", id, run.Status, run.ChangedCount)
		}
		results, err := store.ListRunResults(context.Background(), id)
		if err != nil || len(results) != 2 {
			t.Errorf("run %s: %d results (err %v), want 2", id, len(results), err)
		}
	}
}

// TestRunBatcherShedsPastLimit: a stalled writer must not grow pending without
// bound — past the limit Submit refuses and counts the shed run.
func TestRunBatcherShedsPastLimit(t *testing.T) {
	store := db.NewMemRunStore(100)
	rb := NewRunBatcherWithLimit(store, 2)

	accepted := 0
	for range 5 {
		if rb.Submit(batchRun("envoy-1", "success", 1)) {
			accepted++
		}
	}
	if accepted != 2 {
		t.Errorf("accepted %d runs, want 2 (the limit)", accepted)
	}
	if got := rb.Dropped(); got != 3 {
		t.Errorf("Dropped() = %d, want 3", got)
	}
	rb.Flush(context.Background())
	runs, _ := store.ListAllRuns(context.Background(), 10)
	if len(runs) != 2 {
		t.Errorf("persisted %d runs, want 2", len(runs))
	}
	// Flushing frees the batch: Submit accepts again.
	if !rb.Submit(batchRun("envoy-1", "success", 1)) {
		t.Error("Submit refused after a flush emptied the batch")
	}
}

func TestRunBatcherEmptyFlush(t *testing.T) {
	store := db.NewMemRunStore(100)
	rb := NewRunBatcher(store)

	// Flush with nothing pending — should not error.
	rb.Flush(context.Background())

	runs, err := store.ListAllRuns(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs, got %d", len(runs))
	}
}

func TestRunBatcherShutdown(t *testing.T) {
	store := db.NewMemRunStore(100)
	rb := NewRunBatcher(store)

	rb.Submit(batchRun("envoy-1", "success", 1))

	// Start with a context that we cancel immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rb.Start(ctx, 10*time.Second, 0)

	// After shutdown, the final flush should have persisted the run.
	runs, err := store.ListAllRuns(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run after shutdown flush, got %d", len(runs))
	}
}

func TestRunBatcherConcurrent(t *testing.T) {
	store := db.NewMemRunStore(1000)
	rb := NewRunBatcher(store)

	var wg sync.WaitGroup
	for g := range 10 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 10 {
				rb.Submit(batchRun("envoy-1", "success", int64(g*10+i)))
			}
		}(g)
	}
	wg.Wait()

	rb.Flush(context.Background())

	runs, err := store.ListAllRuns(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 100 {
		t.Fatalf("expected 100 runs, got %d", len(runs))
	}
}
