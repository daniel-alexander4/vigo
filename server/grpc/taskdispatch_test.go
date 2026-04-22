// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
)

func TestPushRouterTrackAndLookup(t *testing.T) {
	router := newTaskRouter(nil)

	router.trackTarget("run-1", "envoy-a", "target-1")
	router.trackTarget("run-1", "envoy-b", "target-2")
	router.trackTarget("run-2", "envoy-a", "target-3")

	// Lookup existing.
	id, ok := router.lookupTarget("run-1", "envoy-a")
	if !ok || id != "target-1" {
		t.Errorf("expected target-1, got %s (ok=%v)", id, ok)
	}

	id, ok = router.lookupTarget("run-1", "envoy-b")
	if !ok || id != "target-2" {
		t.Errorf("expected target-2, got %s (ok=%v)", id, ok)
	}

	id, ok = router.lookupTarget("run-2", "envoy-a")
	if !ok || id != "target-3" {
		t.Errorf("expected target-3, got %s (ok=%v)", id, ok)
	}

	// Lookup non-existent.
	_, ok = router.lookupTarget("run-1", "envoy-c")
	if ok {
		t.Error("expected not found for envoy-c")
	}

	_, ok = router.lookupTarget("run-3", "envoy-a")
	if ok {
		t.Error("expected not found for run-3")
	}
}

func TestPushRouterRemoveTarget(t *testing.T) {
	router := newTaskRouter(nil)

	router.trackTarget("run-1", "envoy-a", "target-1")
	router.trackTarget("run-1", "envoy-b", "target-2")

	router.removeTarget("run-1", "envoy-a")

	_, ok := router.lookupTarget("run-1", "envoy-a")
	if ok {
		t.Error("expected envoy-a to be removed")
	}

	// envoy-b still exists.
	id, ok := router.lookupTarget("run-1", "envoy-b")
	if !ok || id != "target-2" {
		t.Errorf("expected target-2, got %s (ok=%v)", id, ok)
	}

	// Remove last target — should clean up the run entry.
	router.removeTarget("run-1", "envoy-b")
	_, ok = router.lookupTarget("run-1", "envoy-b")
	if ok {
		t.Error("expected envoy-b to be removed")
	}
}

func TestStreamManagerIsConnected(t *testing.T) {
	sm := newStreamManager(0, nil, 0)

	_, cleanup := sm.register("envoy-1", "host-1")
	defer cleanup()

	if !sm.isConnected("envoy-1") {
		t.Error("expected envoy-1 to be connected")
	}

	if sm.isConnected("envoy-2") {
		t.Error("expected envoy-2 to not be connected")
	}
}

func TestStreamManagerConnectedEnvoyIDs(t *testing.T) {
	sm := newStreamManager(0, nil, 0)

	_, cleanup1 := sm.register("envoy-1", "host-1")
	defer cleanup1()
	_, cleanup2 := sm.register("envoy-2", "host-2")
	defer cleanup2()

	ids := sm.connectedEnvoyIDs()
	if len(ids) != 2 {
		t.Errorf("expected 2 connected, got %d", len(ids))
	}
	if !ids["envoy-1"] || !ids["envoy-2"] {
		t.Error("expected both envoy-1 and envoy-2 to be in connected set")
	}
}

func TestStreamManagerDispatchPush(t *testing.T) {
	sm := newStreamManager(0, nil, 0)

	as, cleanup := sm.register("envoy-1", "host-1")
	defer cleanup()

	msg := &pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_TaskDispatch{
			TaskDispatch: &pb.TaskDispatch{
				TaskId:  "push-1",
				Command: "echo test",
			},
		},
	}

	ok := sm.dispatchPush("envoy-1", msg)
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
		if dispatch.TaskId != "push-1" {
			t.Errorf("expected push-1, got %s", dispatch.TaskId)
		}
	default:
		t.Error("expected message on send channel")
	}

	// Dispatch to non-existent envoy.
	ok = sm.dispatchPush("envoy-2", msg)
	if ok {
		t.Error("expected dispatch to non-existent envoy to fail")
	}
}

func TestStreamManagerDispatchCleanup(t *testing.T) {
	sm := newStreamManager(0, nil, 0)

	_, cleanup := sm.register("envoy-1", "host-1")
	cleanup() // Disconnect.

	if sm.isConnected("envoy-1") {
		t.Error("expected envoy-1 to be disconnected after cleanup")
	}
}

func TestResolveTargetsEmptySpec(t *testing.T) {
	_, err := resolveTargets(context.Background(), nil, "", nil)
	if err == nil {
		t.Error("expected error for empty spec")
	}
}
