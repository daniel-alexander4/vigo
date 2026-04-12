// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// openMigratedTestDB returns an in-memory SQLite database with every
// migration in `migrations/*.up.sql` applied in filename order. Replaces
// the per-test hand-rolled CREATE TABLE fixtures, which had a long history
// of drifting from the real schema every time a column was added.
//
// Tests should use this instead of writing their own DDL. If a test needs
// *db.Queries, wrap the return value: `db.New(openMigratedTestDB(t))`.
//
// The helper reads from `../../migrations/` because `go test` runs with
// cwd set to the package directory (`server/api/`). Both `go test ./...`
// from the repo root and `go test ./server/api/...` work.
//
// Migrations marked as no-ops (comment-only files, e.g.
// 000002_rename_compliance_to_convergence_history.up.sql which is
// handled in Go startup code) are skipped.
//
// Postgres-specific migrations under `migrations/postgres/` are never
// applied here — tests run against SQLite in-memory.
func openMigratedTestDB(t *testing.T) *sql.DB {
	t.Helper()

	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	migrationsDir := "../../migrations"
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", migrationsDir, err)
	}

	var upFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		upFiles = append(upFiles, e.Name())
	}
	sort.Strings(upFiles)

	if len(upFiles) == 0 {
		t.Fatalf("no .up.sql migrations found in %s", migrationsDir)
	}

	for _, name := range upFiles {
		path := filepath.Join(migrationsDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if isNoOpMigration(body) {
			continue
		}
		if _, err := sqlDB.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return sqlDB
}

// isNoOpMigration returns true if the file contains only blank lines and
// SQL comments. Used to skip migrations like 000002 that are handled in
// Go startup code rather than as SQL.
func isNoOpMigration(body []byte) bool {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		return false
	}
	return true
}
