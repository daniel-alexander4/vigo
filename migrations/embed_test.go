// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/migrations"
)

// TestEmbeddedMigrationsApplyToLatest exercises the exact production path —
// iofs over the embedded FS + migrate.Up — against a fresh SQLite DB, and
// asserts a post-000018 marker landed. This is the test that would have caught
// the crash: the binary's embedded migrations must reach the schema the code
// expects. (testutil/migratedb.go Execs the SQL directly; it never exercises
// iofs or the migrate library, so this is the only coverage of the real path.)
func TestEmbeddedMigrationsApplyToLatest(t *testing.T) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "test.db")
	m, err := migrate.NewWithSourceInstance("iofs", src, "sqlite3://"+dbPath)
	if err != nil {
		t.Fatalf("NewWithSourceInstance: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name = 'resource_count'`,
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected runs.resource_count after embedded migrations (000018), got %d", n)
	}
}

// TestEmbeddedMatchesOnDisk guards the embed glob: every on-disk migration must
// be embedded, or a future migration silently won't ship in the binary (the
// 000018 failure mode). go test runs with CWD = this package dir, so the disk
// globs read the real migrations/ tree.
func TestEmbeddedMatchesOnDisk(t *testing.T) {
	for _, c := range []struct{ label, pattern string }{
		{"root", "*.sql"},
		{"postgres", "postgres/*.sql"},
	} {
		embedded, err := fs.Glob(migrations.FS, c.pattern)
		if err != nil {
			t.Fatalf("%s: embed glob: %v", c.label, err)
		}
		disk, err := filepath.Glob(c.pattern)
		if err != nil {
			t.Fatalf("%s: disk glob: %v", c.label, err)
		}
		sort.Strings(embedded)
		sort.Strings(disk)
		if len(embedded) == 0 {
			t.Fatalf("%s: no embedded migrations matched %q", c.label, c.pattern)
		}
		if len(embedded) != len(disk) {
			t.Fatalf("%s: embedded=%d on-disk=%d — a migration isn't embedded\nembedded=%v\ndisk=%v",
				c.label, len(embedded), len(disk), embedded, disk)
		}
		for i := range disk {
			if embedded[i] != disk[i] {
				t.Fatalf("%s: mismatch at %d: embedded=%q disk=%q", c.label, i, embedded[i], disk[i])
			}
		}
	}
}
