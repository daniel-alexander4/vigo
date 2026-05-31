// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"testing"

	"git.alexander4.io/vigo/server/testutil"
)

func TestDBRunStore_RecordRun_Success(t *testing.T) {
	ctx := context.Background()
	sqlDB := testutil.OpenMigratedTestDB(t)
	if _, err := sqlDB.ExecContext(ctx,
		"INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?,?,?,?)",
		"env-1", "host-1", "10.0.0.1", "pk"); err != nil {
		t.Fatalf("seed envoy: %v", err)
	}
	store := NewDBRunStore(New(sqlDB), sqlDB)

	run, err := store.RecordRun(ctx, RecordRunParams{
		Run:     CreateRunParams{EnvoyID: "env-1", Status: "running", Trigger: "scheduled", ModuleCount: 2},
		Results: []CreateRunResultParams{{ModuleName: "m1", ResourceType: "file", ResourceName: "r1", ActionTaken: "updated", Changed: 1}, {ModuleName: "m2", ResourceType: "service", ResourceName: "r2", ActionTaken: "settled"}},
		Finish:  FinishRunParams{Status: "success", ChangedCount: 1},
	})
	if err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	if run.ID == "" || run.Status != "success" || run.ChangedCount != 1 {
		t.Fatalf("returned run = %+v, want finished success with 1 change", run)
	}

	// The persisted run is finished (never left in "running"), and all results landed.
	got, err := store.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRunByID: %v", err)
	}
	if got.Status != "success" || !got.FinishedAt.Valid {
		t.Errorf("persisted run = status %q finished=%v, want success + finished", got.Status, got.FinishedAt.Valid)
	}
	results, err := store.ListRunResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunResults: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.RunID != run.ID {
			t.Errorf("result RunID = %q, want %q (RecordRun should stamp it)", r.RunID, run.ID)
		}
	}
}

// TestDBRunStore_RecordRun_RollsBackOnFailure proves the run is atomic: a
// FinishRun that fails (here, a status the CHECK constraint rejects — a
// durably-invalid value, after CreateRun and the result inserts have already
// run inside the tx) rolls back the whole thing. No orphaned "running" row.
func TestDBRunStore_RecordRun_RollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	sqlDB := testutil.OpenMigratedTestDB(t)
	if _, err := sqlDB.ExecContext(ctx,
		"INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?,?,?,?)",
		"env-1", "host-1", "10.0.0.1", "pk"); err != nil {
		t.Fatalf("seed envoy: %v", err)
	}
	store := NewDBRunStore(New(sqlDB), sqlDB)

	_, err := store.RecordRun(ctx, RecordRunParams{
		Run:     CreateRunParams{EnvoyID: "env-1", Status: "running", Trigger: "scheduled", ModuleCount: 1},
		Results: []CreateRunResultParams{{ModuleName: "m1", ResourceType: "file", ResourceName: "r1", ActionTaken: "updated", Changed: 1}},
		Finish:  FinishRunParams{Status: "definitely-not-a-valid-status"},
	})
	if err == nil {
		t.Fatal("expected RecordRun to fail on the CHECK-violating status")
	}

	var runs, results int
	if err := sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs").Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM run_results").Scan(&results); err != nil {
		t.Fatalf("count run_results: %v", err)
	}
	if runs != 0 {
		t.Errorf("after rollback: %d runs persisted, want 0 (no orphaned 'running' row)", runs)
	}
	if results != 0 {
		t.Errorf("after rollback: %d run_results persisted, want 0", results)
	}
}

func TestMemRunStore_RecordRun(t *testing.T) {
	ctx := context.Background()
	store := NewMemRunStore(20)

	run, err := store.RecordRun(ctx, RecordRunParams{
		Run:     CreateRunParams{EnvoyID: "env-1", Status: "running", Trigger: "scheduled", ModuleCount: 2},
		Results: []CreateRunResultParams{{ModuleName: "m1", ResourceType: "file", ResourceName: "r1", ActionTaken: "updated", Changed: 1}, {ModuleName: "m2", ResourceType: "service", ResourceName: "r2", ActionTaken: "settled"}},
		Finish:  FinishRunParams{Status: "success", ChangedCount: 1},
	})
	if err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	if run.ID == "" || run.Status != "success" {
		t.Fatalf("returned run = %+v, want finished success", run)
	}
	got, err := store.GetRunByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRunByID: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("persisted run status = %q, want success", got.Status)
	}
	results, err := store.ListRunResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunResults: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.RunID != run.ID {
			t.Errorf("result RunID = %q, want %q", r.RunID, run.ID)
		}
	}
}

// TestLatestRunPerEnvoy_CarriesFailureCounts proves the fix for the
// compliance-alerter miscount: ListEnvoysWithLatestRun (and thus
// LatestRunPerEnvoy) must surface failed_count + module_count, or a
// Status:"failure" run with those counts dropped is misclassified as
// "converged" by convergence.ComputeConvergence (which keys off FailedCount).
func TestLatestRunPerEnvoy_CarriesFailureCounts(t *testing.T) {
	ctx := context.Background()
	sqlDB := testutil.OpenMigratedTestDB(t)
	if _, err := sqlDB.ExecContext(ctx,
		"INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?,?,?,?)",
		"env-1", "host-1", "10.0.0.1", "pk"); err != nil {
		t.Fatalf("seed envoy: %v", err)
	}
	store := NewDBRunStore(New(sqlDB), sqlDB)

	// A genuinely-failed run: 1 of 3 modules failed.
	if _, err := store.RecordRun(ctx, RecordRunParams{
		Run:    CreateRunParams{EnvoyID: "env-1", Status: "running", Trigger: "scheduled", ModuleCount: 3},
		Finish: FinishRunParams{Status: "failure", ChangedCount: 0, FailedCount: 1},
	}); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	latest, err := store.LatestRunPerEnvoy(ctx, []string{"env-1"})
	if err != nil {
		t.Fatalf("LatestRunPerEnvoy: %v", err)
	}
	run, ok := latest["env-1"]
	if !ok {
		t.Fatal("no latest run for env-1")
	}
	if run.FailedCount != 1 || run.ModuleCount != 3 {
		t.Errorf("LatestRunPerEnvoy dropped counts: FailedCount=%d ModuleCount=%d, want 1 and 3", run.FailedCount, run.ModuleCount)
	}
}
