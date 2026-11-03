// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"sync/atomic"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second, 0)

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
	as, cleanup, _ := sm.register("t1", "web1.example.com")
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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 10*time.Millisecond, 0)

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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second, 0)

	as, cleanup, _ := sm.register("t1", "web1.example.com")
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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second, 0)

	as, cleanup, _ := sm.register("t1", "web1.example.com")
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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second, 0)

	// Register stream first.
	as, cleanup, _ := sm.register("t1", "web1.example.com")
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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second, 0)

	msg1 := testMsg("push-1")
	msg2 := testMsg("push-2")
	sm.queueForPromotion("t1", msg1, nil)
	sm.queueForPromotion("t1", msg2, nil)

	// Register stream — both messages should be delivered.
	as, cleanup, _ := sm.register("t1", "web1.example.com")
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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 5*time.Second, 0)

	as, cleanup, _ := sm.register("t1", "web1.example.com")
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

	sm := newStreamManager(8, fi, metrics.NewTestMetrics(), 10*time.Millisecond, 0)

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

func TestForcePushIncrementsCounterByScope(t *testing.T) {
	fi := fleet.NewFleetIndex()
	for _, id := range []string{"e1", "e2", "e3"} {
		fi.AddEnvoy(fleet.EnvoyState{ID: id, Hostname: id + ".example.com"}, nil)
	}

	m := metrics.NewTestMetrics()
	sm := newStreamManager(8, fi, m, 5*time.Second, 0)

	// Register three streams so broadcast has something to land on.
	for _, id := range []string{"e1", "e2", "e3"} {
		_, cleanup, _ := sm.register(id, id+".example.com")
		defer cleanup()
	}

	// Single-envoy push.
	if got := sm.forcePush("e1"); got != 1 {
		t.Fatalf("single forcePush returned %d, want 1", got)
	}
	if got := testutil.ToFloat64(m.ForcePushesTotal.WithLabelValues("single")); got != 1 {
		t.Errorf("ForcePushesTotal{scope=single} = %v, want 1", got)
	}

	// Broadcast push.
	if got := sm.forcePush(""); got != 3 {
		t.Fatalf("broadcast forcePush returned %d, want 3", got)
	}
	if got := testutil.ToFloat64(m.ForcePushesTotal.WithLabelValues("all")); got != 3 {
		t.Errorf("ForcePushesTotal{scope=all} = %v, want 3", got)
	}

	// Push to an envoy with no registered stream — counter must not move.
	before := testutil.ToFloat64(m.ForcePushesTotal.WithLabelValues("single"))
	if got := sm.forcePush("nonexistent"); got != 0 {
		t.Fatalf("forcePush to unknown returned %d, want 0", got)
	}
	if after := testutil.ToFloat64(m.ForcePushesTotal.WithLabelValues("single")); after != before {
		t.Errorf("counter moved on unsuccessful push: before=%v after=%v", before, after)
	}
}

// TestStreamRegisterCapShedsAndReadmits verifies the held-stream cap
// (checkin.max_connections): it admits up to the limit, sheds the next new
// envoy with admitted=false, re-admits once a slot frees, and never counts a
// reconnecting envoy against the cap.
func TestStreamRegisterCapShedsAndReadmits(t *testing.T) {
	m := metrics.NewTestMetrics()
	sm := newStreamManager(8, nil, m, 5*time.Second, 2) // cap = 2

	// Admit up to the cap.
	_, c1, ok1 := sm.register("e1", "h1")
	_, _, ok2 := sm.register("e2", "h2")
	if !ok1 || !ok2 {
		t.Fatalf("expected first two registers admitted, got %v %v", ok1, ok2)
	}

	// Third new envoy is shed.
	as3, c3, ok3 := sm.register("e3", "h3")
	if ok3 {
		t.Fatal("expected third register to be shed (over cap)")
	}
	if as3 != nil {
		t.Fatal("shed register must return a nil stream")
	}
	c3() // cleanup must be a safe no-op on a shed register
	if got := testutil.ToFloat64(m.CheckinsTotal.WithLabelValues("stream_shed")); got != 1 {
		t.Fatalf("expected 1 stream_shed, got %v", got)
	}

	// A reconnect of an already-held envoy is admitted even at capacity — it
	// replaces its own slot rather than consuming a new one. (Use a fresh
	// register so c1 stays the live cleanup for e1's slot.)
	_, _, okReconnect := sm.register("e2", "h2") // reconnect e2 at capacity
	if !okReconnect {
		t.Fatal("expected reconnect of held envoy to be admitted at capacity")
	}

	// Free e1's slot via its live cleanup, then a new envoy is re-admitted.
	c1()
	if _, _, ok := sm.register("e4", "h4"); !ok {
		t.Fatal("expected re-admit after a slot freed")
	}
}

// TestStreamRegisterNoCap verifies max_connections=0 disables the gate.
func TestStreamRegisterNoCap(t *testing.T) {
	sm := newStreamManager(8, nil, metrics.NewTestMetrics(), 5*time.Second, 0)
	for i := 0; i < 50; i++ {
		if _, _, ok := sm.register(string(rune('a'+i)), "h"); !ok {
			t.Fatalf("register %d shed with no cap configured", i)
		}
	}
}
