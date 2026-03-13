// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"testing"
)

func TestParseBatchSpec_Count(t *testing.T) {
	n, err := parseBatchSpec("5", 20)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("expected 5, got %d", n)
	}
}

func TestParseBatchSpec_Percentage(t *testing.T) {
	n, err := parseBatchSpec("25%", 20)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("expected 5 (25%% of 20), got %d", n)
	}
}

func TestParseBatchSpec_PercentageRoundsUp(t *testing.T) {
	n, err := parseBatchSpec("30%", 7)
	if err != nil {
		t.Fatal(err)
	}
	// 30% of 7 = 2.1, ceil = 3
	if n != 3 {
		t.Errorf("expected 3 (30%% of 7 rounded up), got %d", n)
	}
}

func TestParseBatchSpec_SmallPercentage(t *testing.T) {
	n, err := parseBatchSpec("1%", 2)
	if err != nil {
		t.Fatal(err)
	}
	// 1% of 2 = 0.02, minimum is 1
	if n != 1 {
		t.Errorf("expected 1 (minimum), got %d", n)
	}
}

func TestParseBatchSpec_Empty(t *testing.T) {
	n, err := parseBatchSpec("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 for empty spec, got %d", n)
	}
}

func TestParseBatchSpec_InvalidCount(t *testing.T) {
	_, err := parseBatchSpec("abc", 10)
	if err == nil {
		t.Error("expected error for invalid count")
	}
}

func TestParseBatchSpec_NegativeCount(t *testing.T) {
	_, err := parseBatchSpec("-1", 10)
	if err == nil {
		t.Error("expected error for negative count")
	}
}

func TestParseBatchSpec_ZeroCount(t *testing.T) {
	_, err := parseBatchSpec("0", 10)
	if err == nil {
		t.Error("expected error for zero count")
	}
}

func TestParseBatchSpec_InvalidPercentage(t *testing.T) {
	_, err := parseBatchSpec("0%", 10)
	if err == nil {
		t.Error("expected error for 0%")
	}
}

func TestParseBatchSpec_OverPercentage(t *testing.T) {
	_, err := parseBatchSpec("101%", 10)
	if err == nil {
		t.Error("expected error for >100%")
	}
}

func TestPlanBatches_Single(t *testing.T) {
	targets := makeTargets("a", "b", "c")
	batches := planBatches(targets, 0)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Errorf("expected 3 targets in batch, got %d", len(batches[0]))
	}
}

func TestPlanBatches_LargerThanTotal(t *testing.T) {
	targets := makeTargets("a", "b")
	batches := planBatches(targets, 5)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("expected 2 targets in batch, got %d", len(batches[0]))
	}
}

func TestPlanBatches_Even(t *testing.T) {
	targets := makeTargets("a", "b", "c", "d")
	batches := planBatches(targets, 2)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
	if len(batches[0]) != 2 || len(batches[1]) != 2 {
		t.Errorf("expected [2, 2], got [%d, %d]", len(batches[0]), len(batches[1]))
	}
}

func TestPlanBatches_Uneven(t *testing.T) {
	targets := makeTargets("a", "b", "c", "d", "e")
	batches := planBatches(targets, 2)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("expected 2 in first batch, got %d", len(batches[0]))
	}
	if len(batches[1]) != 2 {
		t.Errorf("expected 2 in second batch, got %d", len(batches[1]))
	}
	if len(batches[2]) != 1 {
		t.Errorf("expected 1 in last batch, got %d", len(batches[2]))
	}
}

func TestPlanBatches_Empty(t *testing.T) {
	batches := planBatches(nil, 2)
	if batches != nil {
		t.Errorf("expected nil for empty targets, got %d batches", len(batches))
	}
}

func TestPlanBatches_SingleTarget(t *testing.T) {
	targets := makeTargets("a")
	batches := planBatches(targets, 1)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 1 {
		t.Errorf("expected 1 target, got %d", len(batches[0]))
	}
}

func TestTaskRouterNotifiers(t *testing.T) {
	router := newTaskRouter(nil)

	// Register a notifier.
	ch := router.registerNotifier("run-1", "thrall-a")

	// Notify completion.
	router.notifyCompletion("run-1", "thrall-a")

	// Channel should be closed.
	select {
	case <-ch:
		// OK — channel closed.
	default:
		t.Error("expected notifier channel to be closed")
	}
}

func TestTaskRouterNotifierRemove(t *testing.T) {
	router := newTaskRouter(nil)

	ch := router.registerNotifier("run-1", "thrall-a")
	router.removeNotifier("run-1", "thrall-a")

	// Notify should be a no-op after removal.
	router.notifyCompletion("run-1", "thrall-a")

	select {
	case <-ch:
		t.Error("channel should not be closed after notifier removal")
	default:
		// OK — channel not closed.
	}
}

func TestTaskRouterNotifierUnknown(t *testing.T) {
	router := newTaskRouter(nil)

	// Should not panic for unknown task/thrall.
	router.notifyCompletion("run-1", "thrall-a")
	router.removeNotifier("run-1", "thrall-a")
}

// makeTargets creates a slice of targetInfo for testing.
func makeTargets(hostnames ...string) []targetInfo {
	targets := make([]targetInfo, len(hostnames))
	for i, h := range hostnames {
		targets[i] = targetInfo{ThrallID: "thrall-" + h, Hostname: h}
	}
	return targets
}
