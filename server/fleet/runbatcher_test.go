// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
)

func TestRunBatcherSubmitAndFlush(t *testing.T) {
	store := db.NewMemRunStore(100)
	rb := NewRunBatcher(store)

	for i := range 3 {
		rb.Submit(RunBatchEntry{
			RunID:        "",
			EnvoyID:     "envoy-1",
			Hostname:     "web-1",
			Status:       "success",
			ModuleCount:  2,
			ChangedCount: 1,
			FailedCount:  0,
			SkippedCount: 1,
			DBResults: []RunBatchResultParam{
				{
					ModuleName:   "nginx",
					ResourceType: "package",
					ResourceName: "nginx",
					ActionTaken:  "none",
					Changed:      false,
				},
				{
					ModuleName:   "nginx",
					ResourceType: "file",
					ResourceName: "/etc/nginx.conf",
					ActionTaken:  "updated",
					Changed:      true,
					DurationMs:   int32(10 * (i + 1)),
				},
			},
		})
	}

	rb.Flush(context.Background())

	// Verify runs were persisted.
	runs, err := store.ListAllRuns(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	for _, run := range runs {
		if run.Status != "success" {
			t.Errorf("expected status success, got %s", run.Status)
		}
		if run.ChangedCount != 1 {
			t.Errorf("expected changed_count 1, got %d", run.ChangedCount)
		}
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

	rb.Submit(RunBatchEntry{
		EnvoyID:    "envoy-1",
		Hostname:    "web-1",
		Status:      "success",
		ModuleCount: 1,
	})

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
				rb.Submit(RunBatchEntry{
					EnvoyID:    "envoy-1",
					Hostname:    "web-1",
					Status:      "success",
					ModuleCount: int64(g*10 + i),
				})
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
