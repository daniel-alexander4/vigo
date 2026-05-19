// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"sync/atomic"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
)

func testMsg(pushID string) *pb.ServerStreamMessage {
	return &pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_TaskDispatch{
			TaskDispatch: &pb.TaskDispatch{TaskId: pushID},
		},
	}
}

func TestStreamPromotionQueueAndDeliver(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second)

	msg := testMsg("push-1")
	sm.queueForPromotion("t1", msg, nil)

	// Verify StreamRequested is set.
	ts := fi.GetEnvoy("t1")
	if ts == nil {
		t.Fatal("envoy not found in FleetIndex")
	}
	if !ts.StreamRequested {
		t.Fatal("expected StreamRequested to be true after queueForPromotion")
	}

	// Register a stream — pending messages should be delivered.
	as, cleanup := sm.register("t1", "web1.example.com")
	defer cleanup()

	select {
	case got := <-as.send:
		if got != msg {
			t.Fatalf("expected queued message, got different message")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for promoted message delivery")
	}

	// StreamRequested should be cleared.
	ts = fi.GetEnvoy("t1")
	if ts.StreamRequested {
		t.Fatal("expected StreamRequested to be cleared after stream registered")
	}

	// Stream should be marked promoted.
	if !as.promoted {
		t.Fatal("expected stream to be marked promoted")
	}
}

func TestStreamPromotionTimeout(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 10*time.Millisecond)

	timedOut := make(chan struct{}, 1)
	sm.queueForPromotion("t1", testMsg("push-1"), func() {
		timedOut <- struct{}{}
	})

	// Verify StreamRequested is set.
	ts := fi.GetEnvoy("t1")
	if !ts.StreamRequested {
		t.Fatal("expected StreamRequested to be true")
	}

	// Wait for timeout callback.
	select {
	case <-timedOut:
		// ok
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for onTimeout callback")
	}

	// StreamRequested should be cleared.
	ts = fi.GetEnvoy("t1")
	if ts.StreamRequested {
		t.Fatal("expected StreamRequested to be cleared after timeout")
	}
}

func TestStreamReleaseOnWorkComplete(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second)

	as, cleanup := sm.register("t1", "web1.example.com")
	defer cleanup()

	as.promoted = true
	atomic.StoreInt32(&as.workCount, 1)

	sm.workCompleted("t1")

	select {
	case got := <-as.send:
		if got.GetStreamRelease() == nil {
			t.Fatalf("expected StreamRelease message, got %T", got.Msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StreamRelease message")
	}
}

func TestStreamReleaseNotSentForUnpromoted(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second)

	as, cleanup := sm.register("t1", "web1.example.com")
	defer cleanup()

	// Stream is NOT promoted (default), workCount is 0.
	sm.workCompleted("t1")

	select {
	case msg := <-as.send:
		t.Fatalf("expected no message on unpromoted stream, got %T", msg.Msg)
	case <-time.After(50 * time.Millisecond):
		// ok — no message sent
	}
}

func TestStreamPromotionToAlreadyConnected(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second)

	// Register stream first.
	as, cleanup := sm.register("t1", "web1.example.com")
	defer cleanup()

	msg := testMsg("push-1")
	sm.queueForPromotion("t1", msg, nil)

	// Message should be delivered immediately.
	select {
	case got := <-as.send:
		if got != msg {
			t.Fatal("expected the queued message to be delivered immediately")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for immediate delivery on connected stream")
	}

	// workCount should be incremented.
	wc := atomic.LoadInt32(&as.workCount)
	if wc != 1 {
		t.Fatalf("expected workCount=1, got %d", wc)
	}
}

func TestStreamPromotionMultipleMessages(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second)

	msg1 := testMsg("push-1")
	msg2 := testMsg("push-2")
	sm.queueForPromotion("t1", msg1, nil)
	sm.queueForPromotion("t1", msg2, nil)

	// Register stream — both messages should be delivered.
	as, cleanup := sm.register("t1", "web1.example.com")
	defer cleanup()

	received := make([]*pb.ServerStreamMessage, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case got := <-as.send:
			received = append(received, got)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for message %d", i+1)
		}
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(received))
	}

	// workCount should be 2.
	wc := atomic.LoadInt32(&as.workCount)
	if wc != 2 {
		t.Fatalf("expected workCount=2, got %d", wc)
	}
}

func TestWorkCompletedMultipleDispatch(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second)

	as, cleanup := sm.register("t1", "web1.example.com")
	defer cleanup()

	as.promoted = true
	atomic.StoreInt32(&as.workCount, 2)

	// First workCompleted — should NOT send StreamRelease (workCount becomes 1).
	sm.workCompleted("t1")

	select {
	case msg := <-as.send:
		t.Fatalf("expected no StreamRelease after first workCompleted, got %T", msg.Msg)
	case <-time.After(50 * time.Millisecond):
		// ok
	}

	// Second workCompleted — should send StreamRelease (workCount becomes 0).
	sm.workCompleted("t1")

	select {
	case got := <-as.send:
		if got.GetStreamRelease() == nil {
			t.Fatalf("expected StreamRelease message, got %T", got.Msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StreamRelease after second workCompleted")
	}
}

func TestStreamPromotionTimeoutClearsFlag(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "t1", Hostname: "web1.example.com"}, nil)

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 10*time.Millisecond)

	timedOut := make(chan struct{}, 1)
	sm.queueForPromotion("t1", testMsg("push-1"), func() {
		timedOut <- struct{}{}
	})

	// Verify flag is set.
	ts := fi.GetEnvoy("t1")
	if !ts.StreamRequested {
		t.Fatal("expected StreamRequested to be true after queue")
	}

	// Wait for timeout.
	select {
	case <-timedOut:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for onTimeout callback")
	}

	// Flag should be cleared.
	ts = fi.GetEnvoy("t1")
	if ts.StreamRequested {
		t.Fatal("expected StreamRequested to be cleared after timeout")
	}

	// No pending entries should remain.
	sm.mu.RLock()
	pending := sm.pending["t1"]
	sm.mu.RUnlock()
	if len(pending) != 0 {
		t.Fatalf("expected no pending entries, got %d", len(pending))
	}
}
