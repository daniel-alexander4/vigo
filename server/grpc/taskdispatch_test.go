// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"context"
	"testing"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

func TestTaskRouterTrackAndLookup(t *testing.T) {
	router := newTaskRouter(nil)

	router.trackTarget("run-1", "thrall-a", "target-1")
	router.trackTarget("run-1", "thrall-b", "target-2")
	router.trackTarget("run-2", "thrall-a", "target-3")

	// Lookup existing.
	id, ok := router.lookupTarget("run-1", "thrall-a")
	if !ok || id != "target-1" {
		t.Errorf("expected target-1, got %s (ok=%v)", id, ok)
	}

	id, ok = router.lookupTarget("run-1", "thrall-b")
	if !ok || id != "target-2" {
		t.Errorf("expected target-2, got %s (ok=%v)", id, ok)
	}

	id, ok = router.lookupTarget("run-2", "thrall-a")
	if !ok || id != "target-3" {
		t.Errorf("expected target-3, got %s (ok=%v)", id, ok)
	}

	// Lookup non-existent.
	_, ok = router.lookupTarget("run-1", "thrall-c")
	if ok {
		t.Error("expected not found for thrall-c")
	}

	_, ok = router.lookupTarget("run-3", "thrall-a")
	if ok {
		t.Error("expected not found for run-3")
	}
}

func TestTaskRouterRemoveTarget(t *testing.T) {
	router := newTaskRouter(nil)

	router.trackTarget("run-1", "thrall-a", "target-1")
	router.trackTarget("run-1", "thrall-b", "target-2")

	router.removeTarget("run-1", "thrall-a")

	_, ok := router.lookupTarget("run-1", "thrall-a")
	if ok {
		t.Error("expected thrall-a to be removed")
	}

	// thrall-b still exists.
	id, ok := router.lookupTarget("run-1", "thrall-b")
	if !ok || id != "target-2" {
		t.Errorf("expected target-2, got %s (ok=%v)", id, ok)
	}

	// Remove last target — should clean up the run entry.
	router.removeTarget("run-1", "thrall-b")
	_, ok = router.lookupTarget("run-1", "thrall-b")
	if ok {
		t.Error("expected thrall-b to be removed")
	}
}

func TestStreamManagerIsConnected(t *testing.T) {
	sm := newStreamManager(0)

	_, cleanup := sm.register("thrall-1", "host-1")
	defer cleanup()

	if !sm.isConnected("thrall-1") {
		t.Error("expected thrall-1 to be connected")
	}

	if sm.isConnected("thrall-2") {
		t.Error("expected thrall-2 to not be connected")
	}
}

func TestStreamManagerConnectedThrallIDs(t *testing.T) {
	sm := newStreamManager(0)

	_, cleanup1 := sm.register("thrall-1", "host-1")
	defer cleanup1()
	_, cleanup2 := sm.register("thrall-2", "host-2")
	defer cleanup2()

	ids := sm.connectedThrallIDs()
	if len(ids) != 2 {
		t.Errorf("expected 2 connected, got %d", len(ids))
	}
	if !ids["thrall-1"] || !ids["thrall-2"] {
		t.Error("expected both thrall-1 and thrall-2 to be in connected set")
	}
}

func TestStreamManagerDispatchTask(t *testing.T) {
	sm := newStreamManager(0)

	as, cleanup := sm.register("thrall-1", "host-1")
	defer cleanup()

	msg := &pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_TaskDispatch{
			TaskDispatch: &pb.TaskDispatch{
				TaskId:  "task-1",
				Command: "echo test",
			},
		},
	}

	ok := sm.dispatchTask("thrall-1", msg)
	if !ok {
		t.Error("expected dispatch to succeed")
	}

	// Verify the message arrived on the send channel.
	select {
	case received := <-as.send:
		dispatch := received.GetTaskDispatch()
		if dispatch == nil {
			t.Fatal("expected TaskDispatch message")
		}
		if dispatch.TaskId != "task-1" {
			t.Errorf("expected task-1, got %s", dispatch.TaskId)
		}
	default:
		t.Error("expected message on send channel")
	}

	// Dispatch to non-existent thrall.
	ok = sm.dispatchTask("thrall-2", msg)
	if ok {
		t.Error("expected dispatch to non-existent thrall to fail")
	}
}

func TestStreamManagerDispatchCleanup(t *testing.T) {
	sm := newStreamManager(0)

	_, cleanup := sm.register("thrall-1", "host-1")
	cleanup() // Disconnect.

	if sm.isConnected("thrall-1") {
		t.Error("expected thrall-1 to be disconnected after cleanup")
	}
}

func TestResolveTargetsEmptySpec(t *testing.T) {
	_, err := resolveTargets(context.Background(), nil, "", nil)
	if err == nil {
		t.Error("expected error for empty spec")
	}
}
