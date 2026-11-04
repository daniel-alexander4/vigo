// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"git.alexander4.io/vigo/migrations"
	vdb "git.alexander4.io/vigo/server/db"
)

// TestIndexes000029_PopulatedDB drives 000029 through the production path
// (iofs + migrate) against a POPULATED database at version 28: rows survive,
// every new index exists, EXPLAIN QUERY PLAN shows each targeted query shape
// actually searching through its index (sql-store S1), and the down migration
// removes them. It also pins the rewritten ListFailedRunsForDate: a range on
// the bare started_at column that includes blocked runs, honours its LIMIT,
// and returns the day's rows — the old date(started_at) = <time.Time> form
// never matched anything.
func TestIndexes000029_PopulatedDB(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db") + "?_foreign_keys=on"
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "sqlite3://"+dsn)
	if err != nil {
		t.Fatalf("NewWithSourceInstance: %v", err)
	}
	defer m.Close()
	if err := m.Migrate(28); err != nil {
		t.Fatalf("migrate to 28: %v", err)
	}

	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	day := time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)
	mustExec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('e1','h1','10.0.0.1','pk')`)
	runs := []struct{ id, status, started string }{
		{"r-midnight", "failure", "2026-11-02 00:00:00"}, // first second of the day: in
		{"r-fail", "failure", "2026-11-02 10:00:00"},
		{"r-blocked", "blocked", "2026-11-02 11:00:00"},
		{"r-ok", "success", "2026-11-02 12:00:00"},
		{"r-prev", "failure", "2026-11-01 23:59:59"}, // previous day: out
		{"r-next", "failure", "2026-11-03 00:00:00"}, // next day's midnight: out
	}
	for _, r := range runs {
		mustExec(`INSERT INTO runs (id, envoy_id, status, started_at) VALUES (?,?,?,?)`, r.id, "e1", r.status, r.started)
	}
	for i := range 40 {
		mustExec(`INSERT INTO run_results (run_id, configcrate_name, resource_type, resource_name, action_taken) VALUES (?,?,?,?,?)`,
			"r-fail", fmt.Sprintf("crate%d", i%5), "file", fmt.Sprintf("/f%d", i), "updated")
		mustExec(`INSERT INTO file_snapshots (envoy_id, file_path, content_hash, run_id) VALUES ('e1',?,?, 'r-fail')`,
			fmt.Sprintf("/f%d", i), "h")
	}

	// The old predicate bound a time.Time against date(started_at): it never
	// matched, so the daily MTTR job always saw zero incidents.
	var oldN int
	if err := sqlDB.QueryRow(`SELECT count(*) FROM runs WHERE date(started_at) = ?1 AND status IN ('failure','partial')`, day).Scan(&oldN); err != nil {
		t.Fatal(err)
	}
	if oldN != 0 {
		t.Fatalf("old ListFailedRunsForDate predicate matched %d rows; the defect this test documents is gone", oldN)
	}

	if err := m.Migrate(29); err != nil {
		t.Fatalf("migrate to 29: %v", err)
	}
	for table, want := range map[string]int{"runs": 6, "run_results": 40, "file_snapshots": 40} {
		var n int
		if err := sqlDB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != want {
			t.Errorf("after 000029: %s has %d rows (err %v), want %d", table, n, err, want)
		}
	}

	newIdx := []string{
		"idx_file_snapshots_run_id", "idx_file_snapshots_envoy_path_created", "idx_file_snapshots_created_at",
		"idx_run_results_created_at", "idx_run_results_configcrate_name",
	}
	for _, idx := range newIdx {
		var n int
		if err := sqlDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after 000029 (n=%d err=%v)", idx, n, err)
		}
	}

	// S1: the planner uses each index for the query shape it was added for.
	plan := func(q string, args ...any) string {
		t.Helper()
		rows, err := sqlDB.Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatalf("explain %s: %v", q, err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			b.WriteString(detail + "\n")
		}
		return b.String()
	}
	for _, c := range []struct{ name, q, want string }{
		{"cascade child lookup", `SELECT id FROM file_snapshots WHERE run_id = 'r-fail'`, "idx_file_snapshots_run_id"},
		{"ListFileSnapshots", `SELECT id FROM file_snapshots WHERE envoy_id = 'e1' AND file_path = '/f1' ORDER BY created_at DESC`, "idx_file_snapshots_envoy_path_created"},
		{"PruneFileSnapshotsByAge", `SELECT t.rowid FROM file_snapshots AS t WHERE t.created_at < '2026-01-01' LIMIT 10`, "idx_file_snapshots_created_at"},
		{"ListRunResultsSince", `SELECT rr.id FROM run_results rr WHERE rr.created_at >= '2026-01-01' ORDER BY rr.created_at DESC LIMIT 10`, "idx_run_results_created_at"},
		{"EnvoysWithConfigcrate", `SELECT rr.run_id FROM run_results rr WHERE rr.configcrate_name = 'crate1'`, "idx_run_results_configcrate_name"},
		{"ListFailedRunsForDate", `SELECT id FROM runs WHERE started_at >= datetime('2026-11-02 00:00:00+00:00') AND started_at < datetime('2026-11-03 00:00:00+00:00') AND status IN ('failure','blocked','partial') ORDER BY started_at ASC LIMIT 10`, "idx_runs_"},
	} {
		if p := plan(c.q); !strings.Contains(p, c.want) {
			t.Errorf("%s: plan does not use %s:\n%s", c.name, c.want, p)
		}
	}

	// The rewritten query through the generated code.
	q := vdb.New(sqlDB)
	got, err := q.ListFailedRunsForDate(context.Background(), vdb.FailedRunsDayParams(day, 100))
	if err != nil {
		t.Fatalf("ListFailedRunsForDate: %v", err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if want := "r-midnight,r-fail,r-blocked"; strings.Join(ids, ",") != want {
		t.Errorf("ListFailedRunsForDate = %v, want %s (day-bounded, blocked included, success excluded)", ids, want)
	}
	if got, _ := q.ListFailedRunsForDate(context.Background(), vdb.FailedRunsDayParams(day, 2)); len(got) != 2 {
		t.Errorf("LIMIT 2 returned %d rows", len(got))
	}

	if err := m.Migrate(28); err != nil {
		t.Fatalf("migrate down to 28: %v", err)
	}
	for _, idx := range newIdx {
		var n int
		if err := sqlDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 0 {
			t.Errorf("index %s survived the down migration (n=%d err=%v)", idx, n, err)
		}
	}
}
