// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
	"github.com/google/uuid"
)

// recordViaReportResult drives the ReportResult handler with a minimal service
// (in-mem run store, no fleetIndex/integrations/queries) and returns the single
// recorded run plus its persisted result rows.
func recordViaReportResult(t *testing.T, req *pb.ReportResultRequest) (db.Run, []db.RunResult) {
	t.Helper()
	store := db.NewMemRunStore(100)
	svc := &AgentService{runStore: store, metrics: metrics.NewTestMetrics()}
	if _, err := svc.ReportResult(context.Background(), req); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	runs, err := store.ListAllRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListAllRuns: err=%v n=%d (want 1)", err, len(runs))
	}
	results, err := store.ListRunResults(context.Background(), runs[0].ID)
	if err != nil {
		t.Fatalf("ListRunResults: %v", err)
	}
	return runs[0], results
}

// TestReportResult_TotalCountElidesNoOps: a newer agent reports the true
// resource total in total_count and sends only the non-no-op rows. The server
// records ResourceCount from total_count, folds the elided settled no-ops into
// skipped, and persists only the sent rows.
func TestReportResult_TotalCountElidesNoOps(t *testing.T) {
	run, results := recordViaReportResult(t, &pb.ReportResultRequest{
		EnvoyId:    uuid.NewString(),
		RunId:      "r1",
		TotalCount: 10, // 10 resources total; 8 settled no-ops elided
		Results: []*pb.ResourceRunResult{
			{ResourceType: "file", ResourceName: "a", ActionTaken: "updated", Changed: true},
			{ResourceType: "package", ResourceName: "b", ActionTaken: "updated", ErrorMessage: "boom"}, // failed
		},
	})
	if run.ResourceCount != 10 {
		t.Errorf("ResourceCount = %d, want 10 (from total_count)", run.ResourceCount)
	}
	if run.ChangedCount != 1 {
		t.Errorf("ChangedCount = %d, want 1", run.ChangedCount)
	}
	if run.FailedCount != 1 {
		t.Errorf("FailedCount = %d, want 1", run.FailedCount)
	}
	if run.SkippedCount != 8 { // 0 settled-in-rows + (10 - 2 sent) elided
		t.Errorf("SkippedCount = %d, want 8 (8 elided no-ops)", run.SkippedCount)
	}
	if run.Status != "failure" {
		t.Errorf("Status = %q, want failure", run.Status)
	}
	if len(results) != 2 {
		t.Errorf("persisted rows = %d, want 2 (only the sent rows)", len(results))
	}
}

// TestReportResult_BlockedPreserved: a blocked resource (action=="blocked", no
// error) survives elision and still produces status="blocked" — the property
// the stream/delta path drops (it filters blocked out and labels it skipped).
func TestReportResult_BlockedPreserved(t *testing.T) {
	run, results := recordViaReportResult(t, &pb.ReportResultRequest{
		EnvoyId:    uuid.NewString(),
		RunId:      "r2",
		TotalCount: 3, // 3 total; 2 settled elided; 1 blocked sent
		Results: []*pb.ResourceRunResult{
			{ResourceType: "exec", ResourceName: "danger", ActionTaken: "blocked"},
		},
	})
	if run.Status != "blocked" {
		t.Errorf("Status = %q, want blocked", run.Status)
	}
	if run.ResourceCount != 3 || run.SkippedCount != 2 {
		t.Errorf("ResourceCount=%d SkippedCount=%d, want 3 and 2", run.ResourceCount, run.SkippedCount)
	}
	if len(results) != 1 || results[0].ActionTaken != "blocked" {
		t.Errorf("persisted rows = %d (want 1 blocked row)", len(results))
	}
}

// TestReportResult_OldAgentUnchanged: an older agent sends every result and
// leaves total_count 0. The server must behave exactly as before — ResourceCount
// from len(results), skipped counted from the settled rows, all rows persisted.
func TestReportResult_OldAgentUnchanged(t *testing.T) {
	run, results := recordViaReportResult(t, &pb.ReportResultRequest{
		EnvoyId: uuid.NewString(),
		RunId:   "r3",
		// TotalCount omitted (0) — pre-elision agent.
		Results: []*pb.ResourceRunResult{
			{ResourceType: "file", ResourceName: "a", ActionTaken: "updated", Changed: true},
			{ResourceType: "file", ResourceName: "b", ActionTaken: "settled"},
			{ResourceType: "file", ResourceName: "c", ActionTaken: "settled"},
		},
	})
	if run.ResourceCount != 3 {
		t.Errorf("ResourceCount = %d, want 3 (fallback to len)", run.ResourceCount)
	}
	if run.ChangedCount != 1 || run.SkippedCount != 2 {
		t.Errorf("ChangedCount=%d SkippedCount=%d, want 1 and 2", run.ChangedCount, run.SkippedCount)
	}
	if len(results) != 3 {
		t.Errorf("persisted rows = %d, want 3 (full set)", len(results))
	}
}
