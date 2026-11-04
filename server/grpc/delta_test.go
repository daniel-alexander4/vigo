// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
)

func newTestDeltaService() (*AgentService, *fleet.FleetIndex, *fleet.RunBatcher) {
	idx := fleet.NewFleetIndex()
	idx.AddEnvoy(fleet.EnvoyState{
		ID:       "envoy-1",
		Hostname: "web-1",
		IP:       "10.0.0.1",
	}, nil)
	store := db.NewMemRunStore(100)
	batcher := fleet.NewRunBatcher(store)
	svc := &AgentService{
		fleetIndex: idx,
		runBatcher: batcher,
		metrics:    metrics.NewTestMetrics(),
	}
	return svc, idx, batcher
}

func TestHandleRunDelta(t *testing.T) {
	svc, idx, batcher := newTestDeltaService()

	delta := &pb.DeltaEvent{
		Sequence: 1,
		Event: &pb.DeltaEvent_Run{
			Run: &pb.RunCompleted{
				RunId:         "run-abc",
				TotalCount:    5, // 2 changed rows below; 3 settled no-ops elided
				BundleVersion: 7,
				Results: []*pb.ResourceRunResult{
					{
						ConfigcrateName: "nginx",
						ResourceType:    "file",
						ResourceName:    "/etc/nginx.conf",
						ActionTaken:     "updated",
						Changed:         true,
						DurationMs:      42,
					},
					{
						ConfigcrateName: "nginx",
						ResourceType:    "service",
						ResourceName:    "nginx",
						ActionTaken:     "restarted",
						Changed:         true,
						DurationMs:      10,
					},
				},
			},
		},
	}

	ack := svc.handleDelta("envoy-1", "web-1", delta)

	// Verify ACK sequence matches.
	if ack.AckedSequence != 1 {
		t.Errorf("expected acked_sequence=1, got %d", ack.AckedSequence)
	}

	// Verify FleetIndex was updated.
	ts := idx.GetEnvoy("envoy-1")
	if ts == nil {
		t.Fatal("envoy not found in index")
	}
	if ts.LastRunStatus != "success" {
		t.Errorf("expected last_run_status=success, got %s", ts.LastRunStatus)
	}
	if ts.LastRunChangedCount != 2 {
		t.Errorf("expected last_run_changed_count=2, got %d", ts.LastRunChangedCount)
	}

	// The handler threads RunCompleted.bundle_version through to the
	// per-envoy convergence cursor (the input to publish→converge latency).
	if ts.LastConvergedVersion != 7 {
		t.Errorf("expected last_converged_version=7, got %d", ts.LastConvergedVersion)
	}

	// Verify recent runs has the entry.
	recent := idx.RecentRuns(10)
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent run, got %d", len(recent))
	}
	// The agent's run_id is never a key: the server mints the id, and the
	// fleet index links (LastRunID + recent ring) name it.
	if recent[0].RunID == "run-abc" || recent[0].RunID == "" {
		t.Errorf("recent run id = %q, want a server-minted id (not the agent's run-abc)", recent[0].RunID)
	}
	if ts.LastRunID != recent[0].RunID {
		t.Errorf("LastRunID %q != recent ring id %q", ts.LastRunID, recent[0].RunID)
	}

	// Verify batcher has pending entry.
	batcher.Flush(context.TODO())
}

// TestHandleRunDelta_FleetIndexIDMatchesDBRow: the delta path publishes the
// run id to the FleetIndex before the async write lands; the persisted row
// must carry that same id (it used to get a fresh DB-minted id, so the
// index's LastRunID / recent-run links pointed at rows that didn't exist).
// File-resource changes on the delta path also produce a file snapshot.
func TestHandleRunDelta_FleetIndexIDMatchesDBRow(t *testing.T) {
	idx := fleet.NewFleetIndex()
	idx.AddEnvoy(fleet.EnvoyState{ID: "envoy-1", Hostname: "web-1", IP: "10.0.0.1"}, nil)
	store := &recordingRunStore{MemRunStore: db.NewMemRunStore(100)}
	batcher := fleet.NewRunBatcher(store)
	svc := &AgentService{fleetIndex: idx, runBatcher: batcher, metrics: metrics.NewTestMetrics()}

	svc.handleDelta("envoy-1", "web-1", &pb.DeltaEvent{Sequence: 1, Event: &pb.DeltaEvent_Run{Run: &pb.RunCompleted{
		RunId:      "agent-run-1",
		TotalCount: 2,
		Results: []*pb.ResourceRunResult{
			{ConfigcrateName: "c", ResourceType: "file", ResourceName: "/etc/x", ActionTaken: "updated", Changed: true, FilePath: "/etc/x", ContentHash: "h1"},
			{ConfigcrateName: "c", ResourceType: "exec", ResourceName: "rm", ActionTaken: "blocked", ErrorMessage: "guardrail"},
		},
	}}})
	batcher.Flush(context.Background())

	ts := idx.GetEnvoy("envoy-1")
	run, err := store.GetRunByID(context.Background(), ts.LastRunID)
	if err != nil {
		t.Fatalf("FleetIndex LastRunID %q has no DB row: %v", ts.LastRunID, err)
	}
	if run.Status != "blocked" || run.FailedCount != 1 {
		t.Errorf("persisted status=%q failed=%d, want blocked/1 (blocked counts as failed)", run.Status, run.FailedCount)
	}
	if len(store.recorded) != 1 || len(store.recorded[0].FileSnapshots) != 1 ||
		store.recorded[0].FileSnapshots[0].FilePath != "/etc/x" {
		t.Errorf("delta run recorded without its file snapshot: %+v", store.recorded)
	}
}

// recordingRunStore captures every RecordRun call (the memory backend keeps
// no file snapshots, so the test reads them off the call).
type recordingRunStore struct {
	*db.MemRunStore
	recorded []db.RecordRunParams
}

func (r *recordingRunStore) RecordRun(ctx context.Context, arg db.RecordRunParams) (db.Run, error) {
	r.recorded = append(r.recorded, arg)
	return r.MemRunStore.RecordRun(ctx, arg)
}

func TestHandleHeartbeat(t *testing.T) {
	svc, idx, _ := newTestDeltaService()

	// Set an old LastSeen via MarkSeen (which marks dirty), then drain to clear.
	old := time.Now().Add(-10 * time.Minute)
	idx.MarkSeen("envoy-1", old)
	idx.DrainDirty() // clear dirty set

	delta := &pb.DeltaEvent{
		Sequence: 5,
		Event:    &pb.DeltaEvent_Heartbeat{Heartbeat: &pb.HeartbeatDelta{}},
	}

	ack := svc.handleDelta("envoy-1", "web-1", delta)

	if ack.AckedSequence != 5 {
		t.Errorf("expected acked_sequence=5, got %d", ack.AckedSequence)
	}

	// Verify LastSeen was updated in-memory.
	ts := idx.GetEnvoy("envoy-1")
	if ts == nil {
		t.Fatal("envoy not found")
	}
	if time.Since(ts.LastSeen) > time.Second {
		t.Errorf("expected LastSeen to be recent, got %v", ts.LastSeen)
	}

	// Heartbeats use MarkSeenInMemory — should NOT mark dirty.
	if dirty := idx.DrainDirty(); len(dirty) != 0 {
		t.Errorf("heartbeat should not mark dirty, got %d dirty entries", len(dirty))
	}
}

func TestDeltaAckSequence(t *testing.T) {
	svc, _, _ := newTestDeltaService()

	for seq := int64(1); seq <= 5; seq++ {
		delta := &pb.DeltaEvent{
			Sequence: seq,
			Event:    &pb.DeltaEvent_Heartbeat{Heartbeat: &pb.HeartbeatDelta{}},
		}
		ack := svc.handleDelta("envoy-1", "web-1", delta)
		if ack.AckedSequence != seq {
			t.Errorf("expected acked_sequence=%d, got %d", seq, ack.AckedSequence)
		}
	}
}

func TestDeltaRunFailure(t *testing.T) {
	svc, idx, _ := newTestDeltaService()

	delta := &pb.DeltaEvent{
		Sequence: 1,
		Event: &pb.DeltaEvent_Run{
			Run: &pb.RunCompleted{
				RunId:      "run-fail",
				TotalCount: 3, // 1 failed row below; 2 settled no-ops elided
				Results: []*pb.ResourceRunResult{
					{
						ConfigcrateName: "apt",
						ResourceType:    "package",
						ResourceName:    "broken-pkg",
						ActionTaken:     "install",
						ErrorMessage:    "E: Unable to locate package",
					},
				},
			},
		},
	}

	svc.handleDelta("envoy-1", "web-1", delta)

	ts := idx.GetEnvoy("envoy-1")
	if ts.LastRunStatus != "failure" {
		t.Errorf("expected last_run_status=failure, got %s", ts.LastRunStatus)
	}
}

// TestDeltaRunBlocked verifies the delta path now surfaces a guardrail-blocked
// resource as status="blocked" — previously build_run_delta dropped the blocked
// row and handleRunDelta counted it as skipped (never "blocked").
func TestDeltaRunBlocked(t *testing.T) {
	svc, idx, _ := newTestDeltaService()

	delta := &pb.DeltaEvent{
		Sequence: 1,
		Event: &pb.DeltaEvent_Run{
			Run: &pb.RunCompleted{
				RunId:      "run-blocked",
				TotalCount: 4, // 1 blocked row below; 3 settled no-ops elided
				Results: []*pb.ResourceRunResult{
					{
						ConfigcrateName: "hardening",
						ResourceType:    "exec",
						ResourceName:    "rm-rf-danger",
						ActionTaken:     "blocked",
						ErrorMessage:    "guardrail: dangerous command",
					},
				},
			},
		},
	}

	svc.handleDelta("envoy-1", "web-1", delta)

	ts := idx.GetEnvoy("envoy-1")
	if ts.LastRunStatus != "blocked" {
		t.Errorf("expected last_run_status=blocked, got %s", ts.LastRunStatus)
	}
}
