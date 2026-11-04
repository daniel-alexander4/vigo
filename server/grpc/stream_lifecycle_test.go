// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/testutil"
)

// TestRevokeEnvoyClosesHeldStream: revoke used to only flip the revoked bit,
// and the per-message re-check fires only when the envoy next sends — pushes
// kept reaching a revoked envoy's held stream. Revoke must end the stream.
// DeleteEnvoy, the sibling, must too.
func TestRevokeEnvoyClosesHeldStream(t *testing.T) {
	for _, op := range []string{"revoke", "delete"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			queries := db.New(testutil.OpenMigratedTestDB(t))
			e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: op + ".example.com", IpAddress: "10.0.0.9", PubkeyPem: "pk"})
			if err != nil {
				t.Fatal(err)
			}
			fi := fleet.NewFleetIndex()
			fi.AddEnvoy(fleet.EnvoyState{ID: e.ID, Hostname: e.Hostname}, nil)
			sm := newStreamManager(4, fi, metrics.NewTestMetrics(), time.Minute, 0)
			as, cleanup, ok := sm.register(e.ID, e.Hostname)
			if !ok {
				t.Fatal("register refused")
			}
			defer cleanup()

			admin := &AdminService{queries: queries, fleetIndex: fi, closeStream: sm.closeStream}
			if op == "revoke" {
				_, err = admin.RevokeEnvoy(ctx, &pb.RevokeEnvoyRequest{Id: e.ID, SkipSwarmCleanup: true})
			} else {
				_, err = admin.DeleteEnvoy(ctx, &pb.DeleteEnvoyRequest{Id: e.ID, SkipSwarmCleanup: true})
			}
			if err != nil {
				t.Fatalf("%s: %v", op, err)
			}
			select {
			case <-as.done:
			default:
				t.Fatalf("%s left the held stream open (done not closed)", op)
			}
			if sm.isConnected(e.ID) {
				t.Errorf("%s left the envoy in the stream map", op)
			}
		})
	}
}

// TestStreamDisconnect_FailsInFlightTargets: a target dispatched on a stream
// that then drops was never removed — the pending entry and the task_run stayed
// "running". The stream's departure must fail exactly that envoy's targets and
// release any rolling-execution waiter; a reconnect that replaced the stream
// must not.
func TestStreamDisconnect_FailsInFlightTargets(t *testing.T) {
	sm := newStreamManager(4, nil, metrics.NewTestMetrics(), time.Minute, 0)
	r := newTaskRouter(nil, metrics.NewTestMetrics())
	var fired atomic.Int32
	sm.onDisconnect = func(envoyID string) {
		fired.Add(1)
		r.failEnvoyTargets(context.Background(), envoyID, "stream closed")
	}

	r.trackTarget("task-1", "env-a", "t1a")
	r.trackTarget("task-1", "env-b", "t1b")
	r.trackTarget("task-2", "env-a", "t2a")
	waiter := r.registerNotifier("task-1", "env-a")

	// A reconnect replaces env-a's stream: the old stream's cleanup must not
	// fail the targets now riding the new stream.
	_, oldCleanup, _ := sm.register("env-a", "a")
	_, newCleanup, _ := sm.register("env-a", "a")
	oldCleanup()
	if fired.Load() != 0 {
		t.Fatalf("replaced stream's cleanup fired onDisconnect")
	}
	if _, ok := r.lookupTarget("task-1", "env-a"); !ok {
		t.Fatal("replaced stream's cleanup dropped a target the new stream carries")
	}

	newCleanup()
	if fired.Load() != 1 {
		t.Fatalf("onDisconnect fired %d times on the current stream's cleanup, want 1", fired.Load())
	}
	for _, task := range []string{"task-1", "task-2"} {
		if _, ok := r.lookupTarget(task, "env-a"); ok {
			t.Errorf("%s/env-a still pending after its stream dropped", task)
		}
	}
	if _, ok := r.lookupTarget("task-1", "env-b"); !ok {
		t.Error("env-b's target was failed by env-a's disconnect")
	}
	select {
	case <-waiter:
	default:
		t.Error("rolling-execution waiter for env-a was not released")
	}
}

// TestStreamDisconnect_MarksTargetFailedInDB: the orphaned target row is
// finished as failed and its task run finalized.
func TestStreamDisconnect_MarksTargetFailedInDB(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testutil.OpenMigratedTestDB(t))
	e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "td.example.com", IpAddress: "10.0.0.8", PubkeyPem: "pk"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := queries.CreateTaskRun(ctx, db.CreateTaskRunParams{Command: "true", TargetGlob: "*", TotalTargets: 1, TimeoutSecs: 60})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.UpdateTaskRunStatus(ctx, db.UpdateTaskRunStatusParams{ID: run.ID, Status: "running", StartedAt: sql.NullTime{Time: time.Now(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	target, err := queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{TaskRunID: run.ID, EnvoyID: e.ID, Hostname: e.Hostname, Status: "dispatched"})
	if err != nil {
		t.Fatal(err)
	}
	r := newTaskRouter(queries, metrics.NewTestMetrics())
	r.trackTarget(run.ID, e.ID, target.ID)

	if n := r.failEnvoyTargets(ctx, e.ID, "stream closed"); n != 1 {
		t.Fatalf("failEnvoyTargets = %d, want 1", n)
	}
	counts, err := queries.CountTaskRunTargetsByStatus(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Status != "failed" {
		t.Errorf("target statuses = %+v, want one failed", counts)
	}
	tr, err := queries.GetTaskRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != "failed" {
		t.Errorf("task run status = %q, want failed (finalized)", tr.Status)
	}
}

// TestAgentStream_UnknownTaskResultDoesNotReleaseStream: workCompleted used to
// be decremented for any TaskResult/QueryResponse, so a stray or duplicate one
// for an unknown id released a promoted stream while real work was pending.
func TestAgentStream_UnknownTaskResultDoesNotReleaseStream(t *testing.T) {
	const envoyID = "envoy-work-1"
	idx := fleet.NewFleetIndex()
	idx.AddEnvoy(fleet.EnvoyState{ID: envoyID, Hostname: "w1"}, nil)
	svc := &AgentService{
		fleetIndex:  idx,
		metrics:     metrics.NewTestMetrics(),
		streamMgr:   newStreamManager(8, idx, metrics.NewTestMetrics(), time.Minute, 0),
		taskRouter:  newTaskRouter(nil, metrics.NewTestMetrics()),
		queryRouter: newQueryRouter(),
	}
	as, cleanup, _ := svc.streamMgr.register(envoyID, "w1")
	defer cleanup()
	svc.streamMgr.mu.Lock()
	as.promoted = true
	as.workCount = 1 // one real dispatch outstanding
	svc.streamMgr.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &overlapStream{ctx: ctx, recv: make(chan *pb.AgentStreamMessage, 4)}
	go func() {
		_ = svc.serveAgentStream(fs, &serialSender{stream: fs}, as, envoyID, db.Envoy{ID: envoyID, Hostname: "w1"}, false)
	}()
	fs.recv <- &pb.AgentStreamMessage{Msg: &pb.AgentStreamMessage_TaskResult{TaskResult: &pb.TaskResult{TaskId: "nope"}}}
	fs.recv <- &pb.AgentStreamMessage{Msg: &pb.AgentStreamMessage_QueryResponse{QueryResponse: &pb.QueryResponse{QueryId: "nope"}}}
	fs.recv <- &pb.AgentStreamMessage{Msg: &pb.AgentStreamMessage_TaskAck{TaskAck: &pb.TaskAck{TaskId: "nope"}}}

	deadline := time.Now().Add(time.Second)
	for len(fs.recv) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the reader finish the last message
	if got := atomic.LoadInt32(&as.workCount); got != 1 {
		t.Errorf("workCount = %d after unknown task/query messages, want 1", got)
	}
	if fs.sent.Load() != 0 {
		t.Errorf("stream sent %d messages (a StreamRelease?), want none", fs.sent.Load())
	}
}

// TestStreamManager_ConcurrentRegisterNoRace: register read len(sm.streams)
// and as.promoted after dropping the lock; under -race, concurrent registers,
// cleanups and workCompleted calls must be clean.
func TestStreamManager_ConcurrentRegisterNoRace(t *testing.T) {
	sm := newStreamManager(4, nil, metrics.NewTestMetrics(), time.Minute, 0)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 50 {
				id := fmt.Sprintf("env-%d", (g+i)%3) // shared across goroutines
				_, cleanup, _ := sm.register(id, id)
				sm.queueForPromotion(id, &pb.ServerStreamMessage{}, nil)
				sm.workCompleted(id)
				cleanup()
			}
		}(g)
	}
	wg.Wait()
}
