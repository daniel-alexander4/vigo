// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"testing"

	"git.alexander4.io/vigo/server/testutil"
)

// TestMigrationsConverge applies the full migration chain (000001 → latest) and
// asserts the schema lands where the code expects. 000008 renames push_runs →
// task_runs and adds runs.trigger; 000001 only ever creates push_runs, so this
// is the exact path a long-lived database stuck at version 7 takes when 000008
// is applied — proving fresh and lagging databases converge to one schema.
func TestMigrationsConverge(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)

	tableExists := func(name string) bool {
		var n int
		if err := sqlDB.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&n); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", name, err)
		}
		return n > 0
	}
	columnExists := func(table, col string) bool {
		rows, err := sqlDB.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			if name == col {
				return true
			}
		}
		return false
	}

	for _, tbl := range []string{"task_runs", "task_run_targets"} {
		if !tableExists(tbl) {
			t.Errorf("table %q missing after migrations — 000008 rename did not apply", tbl)
		}
	}
	for _, tbl := range []string{"push_runs", "push_run_targets"} {
		if tableExists(tbl) {
			t.Errorf("table %q still present — 000008 rename did not apply", tbl)
		}
	}
	if !columnExists("runs", "trigger") {
		t.Error("runs.trigger column missing after migrations")
	}
}
