// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"git.alexander4.io/vigo/migrations"
)

// TestRunsStatusBlocked_RebuildPreservesPopulatedRows drives 000028 through the
// production path (iofs + migrate) against a POPULATED database at version 27,
// with foreign keys both on (the server's DSN) and off. runs is a parent of
// run_results and file_snapshots (ON DELETE CASCADE), so a careless rebuild
// would cascade-delete every child row — the row counts and FK targets are
// what this guards. It then proves 'blocked' is accepted, and that the down
// migration folds it back to 'failure' without losing rows.
func TestRunsStatusBlocked_RebuildPreservesPopulatedRows(t *testing.T) {
	for _, fk := range []string{"on", "off"} {
		t.Run("foreign_keys="+fk, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "test.db")
			dsn := dbPath + "?_foreign_keys=" + fk
			src, err := iofs.New(migrations.FS, ".")
			if err != nil {
				t.Fatalf("iofs.New: %v", err)
			}
			m, err := migrate.NewWithSourceInstance("iofs", src, "sqlite3://"+dsn)
			if err != nil {
				t.Fatalf("NewWithSourceInstance: %v", err)
			}
			defer m.Close()
			if err := m.Migrate(27); err != nil {
				t.Fatalf("migrate to 27: %v", err)
			}

			db, err := sql.Open("sqlite3", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			mustExec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.Exec(q, args...); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			mustExec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('e1','h1','10.0.0.1','pk')`)
			mustExec(`INSERT INTO runs (id, envoy_id, status, resource_count, trigger) VALUES ('r1','e1','success',2,'force-push')`)
			mustExec(`INSERT INTO runs (id, envoy_id, status) VALUES ('r2','e1','failure')`)
			mustExec(`INSERT INTO run_results (id, run_id, configcrate_name, resource_type, resource_name, action_taken) VALUES ('x1','r1','c','file','/a','updated')`)
			mustExec(`INSERT INTO run_results (id, run_id, configcrate_name, resource_type, resource_name, action_taken) VALUES ('x2','r2','c','file','/b','failed')`)
			mustExec(`INSERT INTO file_snapshots (id, envoy_id, file_path, content_hash, run_id) VALUES ('f1','e1','/a','h','r1')`)

			// Old CHECK rejects 'blocked' — the defect this migration fixes.
			if _, err := db.Exec(`UPDATE runs SET status='blocked' WHERE id='r2'`); err == nil {
				t.Fatal("version 27 accepted status 'blocked'; the precondition this test relies on is gone")
			}

			if err := m.Migrate(28); err != nil {
				t.Fatalf("migrate to 28: %v", err)
			}

			count := func(table string) int {
				t.Helper()
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
					t.Fatalf("count %s: %v", table, err)
				}
				return n
			}
			for table, want := range map[string]int{"runs": 2, "run_results": 2, "file_snapshots": 1} {
				if got := count(table); got != want {
					t.Errorf("after 000028: %s has %d rows, want %d (rebuild lost rows)", table, got, want)
				}
			}
			var trig string
			if err := db.QueryRow(`SELECT trigger FROM runs WHERE id='r1'`).Scan(&trig); err != nil || trig != "force-push" {
				t.Errorf("runs.trigger after rebuild = %q (err %v), want force-push", trig, err)
			}
			for _, child := range []string{"run_results", "file_snapshots"} {
				var parent string
				if err := db.QueryRow(`SELECT "table" FROM pragma_foreign_key_list(?) WHERE "from"='run_id'`, child).Scan(&parent); err != nil {
					t.Fatalf("fk list %s: %v", child, err)
				}
				if parent != "runs" {
					t.Errorf("%s.run_id references %q, want runs", child, parent)
				}
			}
			for _, idx := range []string{"idx_runs_envoy_id", "idx_runs_envoy_started_status", "idx_runs_started_at", "idx_run_results_run_id"} {
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
					t.Errorf("index %s missing after 000028 (n=%d err=%v)", idx, n, err)
				}
			}
			var fkBad int
			if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fkBad); err != nil || fkBad != 0 {
				t.Errorf("foreign_key_check after 000028: %d violations (err %v)", fkBad, err)
			}

			mustExec(`UPDATE runs SET status='blocked' WHERE id='r2'`)

			// Cascade still wired to the rebuilt parent (FK-on only: with
			// foreign keys off SQLite never cascades).
			if fk == "on" {
				mustExec(`INSERT INTO runs (id, envoy_id, status) VALUES ('r3','e1','running')`)
				mustExec(`INSERT INTO run_results (id, run_id, configcrate_name, resource_type, resource_name, action_taken) VALUES ('x3','r3','c','file','/c','updated')`)
				mustExec(`DELETE FROM runs WHERE id='r3'`)
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM run_results WHERE id='x3'`).Scan(&n); err != nil || n != 0 {
					t.Errorf("ON DELETE CASCADE lost in rebuild: child row survived (n=%d err=%v)", n, err)
				}
			}

			if err := m.Migrate(27); err != nil {
				t.Fatalf("migrate down to 27: %v", err)
			}
			var st string
			if err := db.QueryRow(`SELECT status FROM runs WHERE id='r2'`).Scan(&st); err != nil || st != "failure" {
				t.Errorf("down: blocked run status = %q (err %v), want failure", st, err)
			}
			for table, want := range map[string]int{"runs": 2, "run_results": 2, "file_snapshots": 1} {
				if got := count(table); got != want {
					t.Errorf("after down: %s has %d rows, want %d", table, got, want)
				}
			}
		})
	}
}
