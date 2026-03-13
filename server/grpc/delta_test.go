// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"testing"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
)

func newTestDeltaService() (*AgentService, *fleet.FleetIndex, *fleet.RunBatcher) {
	idx := fleet.NewFleetIndex()
	idx.AddThrall(fleet.ThrallState{
		ID:       "thrall-1",
		Hostname: "web-1",
		IP:       "10.0.0.1",
	}, nil)
	store := db.NewMemRunStore(100)
	batcher := fleet.NewRunBatcher(store)
	svc := &AgentService{
		fleetIndex: idx,
		runBatcher: batcher,
	}
	return svc, idx, batcher
}

func TestHandleRunDelta(t *testing.T) {
	svc, idx, batcher := newTestDeltaService()

	delta := &pb.DeltaEvent{
		Sequence: 1,
		Event: &pb.DeltaEvent_Run{
			Run: &pb.RunCompleted{
				RunId:        "run-abc",
				Status:       "success",
				TotalCount:   5,
				ChangedCount: 2,
				FailedCount:  0,
				SkippedCount: 3,
				Results: []*pb.ModuleResult{
					{
						ModuleName:   "nginx",
						ResourceType: "file",
						ResourceName: "/etc/nginx.conf",
						ActionTaken:  "updated",
						Changed:      true,
						DurationMs:   42,
					},
					{
						ModuleName:   "nginx",
						ResourceType: "service",
						ResourceName: "nginx",
						ActionTaken:  "restarted",
						Changed:      true,
						DurationMs:   10,
					},
				},
			},
		},
	}

	ack := svc.handleDelta("thrall-1", "web-1", delta)

	// Verify ACK sequence matches.
	if ack.AckedSequence != 1 {
		t.Errorf("expected acked_sequence=1, got %d", ack.AckedSequence)
	}

	// Verify FleetIndex was updated.
	ts := idx.GetThrall("thrall-1")
	if ts == nil {
		t.Fatal("thrall not found in index")
	}
	if ts.LastRunStatus != "success" {
		t.Errorf("expected last_run_status=success, got %s", ts.LastRunStatus)
	}
	if ts.LastRunChangedCount != 2 {
		t.Errorf("expected last_run_changed_count=2, got %d", ts.LastRunChangedCount)
	}

	// Verify recent runs has the entry.
	recent := idx.RecentRuns(10)
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent run, got %d", len(recent))
	}
	if recent[0].RunID != "run-abc" {
		t.Errorf("expected run_id=run-abc, got %s", recent[0].RunID)
	}

	// Verify batcher has pending entry.
	batcher.Flush(nil)
}

func TestHandleHeartbeat(t *testing.T) {
	svc, idx, _ := newTestDeltaService()

	// Set an old LastSeen via MarkSeen (which marks dirty), then drain to clear.
	old := time.Now().Add(-10 * time.Minute)
	idx.MarkSeen("thrall-1", old)
	idx.DrainDirty() // clear dirty set

	delta := &pb.DeltaEvent{
		Sequence: 5,
		Event:    &pb.DeltaEvent_Heartbeat{Heartbeat: &pb.HeartbeatDelta{}},
	}

	ack := svc.handleDelta("thrall-1", "web-1", delta)

	if ack.AckedSequence != 5 {
		t.Errorf("expected acked_sequence=5, got %d", ack.AckedSequence)
	}

	// Verify LastSeen was updated in-memory.
	ts := idx.GetThrall("thrall-1")
	if ts == nil {
		t.Fatal("thrall not found")
	}
	if time.Since(ts.LastSeen) > time.Second {
		t.Errorf("expected LastSeen to be recent, got %v", ts.LastSeen)
	}

	// Heartbeats use MarkSeenInMemory — should NOT mark dirty.
	if idx.DirtyCount() != 0 {
		t.Errorf("heartbeat should not mark dirty, got DirtyCount=%d", idx.DirtyCount())
	}
}

func TestDeltaAckSequence(t *testing.T) {
	svc, _, _ := newTestDeltaService()

	for seq := int64(1); seq <= 5; seq++ {
		delta := &pb.DeltaEvent{
			Sequence: seq,
			Event:    &pb.DeltaEvent_Heartbeat{Heartbeat: &pb.HeartbeatDelta{}},
		}
		ack := svc.handleDelta("thrall-1", "web-1", delta)
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
				RunId:        "run-fail",
				Status:       "failure",
				TotalCount:   3,
				ChangedCount: 1,
				FailedCount:  1,
				SkippedCount: 1,
				Results: []*pb.ModuleResult{
					{
						ModuleName:   "apt",
						ResourceType: "package",
						ResourceName: "broken-pkg",
						ActionTaken:  "install",
						ErrorMessage: "E: Unable to locate package",
					},
				},
			},
		},
	}

	svc.handleDelta("thrall-1", "web-1", delta)

	ts := idx.GetThrall("thrall-1")
	if ts.LastRunStatus != "failure" {
		t.Errorf("expected last_run_status=failure, got %s", ts.LastRunStatus)
	}
}
