// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"google.golang.org/grpc"
)

// overlapStream is a fake AgentStream server stream whose Send detects
// overlapping calls — gRPC-go forbids concurrent SendMsg on one stream. Send
// holds the call open briefly to widen the window, and also bumps a plain
// (unsynchronized) counter so `go test -race` reports any overlap too.
type overlapStream struct {
	grpc.ServerStream
	ctx       context.Context
	recv      chan *pb.AgentStreamMessage
	inflight  atomic.Int32
	overlaps  atomic.Int32
	sent      atomic.Int32
	unguarded int
}

func (f *overlapStream) Context() context.Context { return f.ctx }

func (f *overlapStream) Send(*pb.ServerStreamMessage) error {
	if f.inflight.Add(1) > 1 {
		f.overlaps.Add(1)
	}
	f.unguarded++
	time.Sleep(20 * time.Microsecond)
	f.inflight.Add(-1)
	f.sent.Add(1)
	return nil
}

func (f *overlapStream) Recv() (*pb.AgentStreamMessage, error) {
	select {
	case m := <-f.recv:
		return m, nil
	case <-f.ctx.Done():
		return nil, io.EOF
	}
}

// TestAgentStream_PushesAndRepliesNeverSendConcurrently drives the real held-
// stream loop with server pushes (writer goroutine) racing in-stream delta acks
// (reader goroutine) on one stream, and requires no two Sends to overlap.
func TestAgentStream_PushesAndRepliesNeverSendConcurrently(t *testing.T) {
	const envoyID = "envoy-send-1"
	idx := fleet.NewFleetIndex()
	idx.AddEnvoy(fleet.EnvoyState{ID: envoyID, Hostname: "web-send-1"}, nil)
	svc := &AgentService{fleetIndex: idx, metrics: metrics.NewTestMetrics()}

	const n = 300
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &overlapStream{ctx: ctx, recv: make(chan *pb.AgentStreamMessage, n)}
	as := &agentStream{
		envoyID:  envoyID,
		hostname: "web-send-1",
		send:     make(chan *pb.ServerStreamMessage, n),
		done:     make(chan struct{}),
	}

	errc := make(chan error, 1)
	go func() {
		errc <- svc.serveAgentStream(fs, &serialSender{stream: fs}, as, envoyID, db.Envoy{ID: envoyID, Hostname: "web-send-1"}, false)
	}()

	go func() {
		for i := 0; i < n; i++ {
			as.send <- &pb.ServerStreamMessage{Msg: &pb.ServerStreamMessage_ForcePush{ForcePush: &pb.StreamForcePush{}}}
		}
	}()
	go func() {
		for i := 0; i < n; i++ {
			fs.recv <- &pb.AgentStreamMessage{Msg: &pb.AgentStreamMessage_Delta{Delta: &pb.DeltaEvent{
				Sequence: int64(i + 1),
				Event:    &pb.DeltaEvent_Heartbeat{Heartbeat: &pb.HeartbeatDelta{}},
			}}}
		}
	}()

	deadline := time.Now().Add(20 * time.Second)
	for fs.sent.Load() < 2*n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d sends completed", fs.sent.Load(), 2*n)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-errc

	if got := fs.overlaps.Load(); got != 0 {
		t.Fatalf("%d overlapping stream.Send calls — pushes and in-stream replies must be serialized", got)
	}
}
