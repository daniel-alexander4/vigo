// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

var testDBCounter atomic.Int64

// OpenMigratedTestDB returns an in-memory SQLite database with every
// migration in migrations/*.up.sql applied in filename order. Replaces
// per-test hand-rolled CREATE TABLE fixtures, which drift from the real
// schema every time a column is added.
//
// Tests should use this instead of writing their own DDL. Wrap the
// return value with db.New() when *db.Queries is needed.
//
// The migrations directory is resolved relative to this source file so
// the path is correct regardless of which package's test binary calls us.
//
// Postgres-specific migrations under migrations/postgres/ are never
// applied — tests run against SQLite in-memory.
func OpenMigratedTestDB(t *testing.T) *sql.DB {
	t.Helper()

	// Use a unique shared-memory name so connection pools within a single
	// test share one database (needed for concurrent access), while
	// separate tests get isolation.
	n := testDBCounter.Add(1)
	dsn := fmt.Sprintf("file:testdb_%d?mode=memory&cache=shared&_foreign_keys=on", n)
	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	// Resolve migrations/ relative to this source file.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

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
// SQL comments. Used to skip migrations that are handled in Go startup
// code rather than as SQL.
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
