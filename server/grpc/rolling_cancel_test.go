// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/testutil"
)

// TestRollingExecution_StopsOnCancel: the batch loop used to ignore a REST
// cancel / workflow abort and keep dispatching every remaining batch, and the
// final maybeFinishTaskRun rewrote the cancelled run to complete/failed. A
// cancelled run must dispatch nothing more and keep its cancelled status.
func TestRollingExecution_StopsOnCancel(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testutil.OpenMigratedTestDB(t))
	m := metrics.NewTestMetrics()
	svc := &AgentService{
		queries:    queries,
		metrics:    m,
		streamMgr:  newStreamManager(4, nil, m, time.Minute, 0),
		taskRouter: newTaskRouter(queries, m),
	}

	run, err := queries.CreateTaskRun(ctx, db.CreateTaskRunParams{Command: "true", TargetGlob: "*", Operator: "t", TotalTargets: 2, TimeoutSecs: 1})
	if err != nil {
		t.Fatal(err)
	}
	batches := make([][]targetRecord, 2)
	for i, h := range []string{"a.example.com", "b.example.com"} {
		e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: h, IpAddress: "10.0.0.9", PubkeyPem: "pk"})
		if err != nil {
			t.Fatal(err)
		}
		tg, err := queries.CreateTaskRunTarget(ctx, db.CreateTaskRunTargetParams{TaskRunID: run.ID, EnvoyID: e.ID, Hostname: h, Status: "pending"})
		if err != nil {
			t.Fatal(err)
		}
		batches[i] = []targetRecord{{info: targetInfo{EnvoyID: e.ID, Hostname: h}, targetID: tg.ID}}
	}
	if err := queries.UpdateTaskRunStatus(ctx, db.UpdateTaskRunStatusParams{ID: run.ID, Status: "running", StartedAt: sql.NullTime{Time: time.Now(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CancelTaskRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := queries.CancelTaskRunTargets(ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	svc.runRollingExecution(run.ID, batches, "true", 1, 0, "", "", nil, 2, "")

	targets, err := queries.ListTaskRunTargets(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		// An attempted dispatch to an unconnected envoy marks it "offline".
		if tg.Status != "skipped" {
			t.Errorf("target %s: status %q after cancel, want skipped (a batch was dispatched)", tg.Hostname, tg.Status)
		}
	}
	if got, _ := queries.GetTaskRun(ctx, run.ID); got.Status != "cancelled" {
		t.Errorf("task run status %q after rolling loop, want cancelled", got.Status)
	}

	// A late target completion on a cancelled run must not rewrite it.
	svc.taskRouter.maybeFinishTaskRun(ctx, run.ID)
	if got, _ := queries.GetTaskRun(ctx, run.ID); got.Status != "cancelled" {
		t.Errorf("maybeFinishTaskRun rewrote cancelled run to %q", got.Status)
	}
}
