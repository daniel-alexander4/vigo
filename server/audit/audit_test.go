// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/db"
)

func setupTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE audit_entries (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			prev_hash   TEXT NOT NULL DEFAULT '',
			hash        TEXT NOT NULL,
			timestamp   DATETIME NOT NULL DEFAULT (datetime('now')),
			event_type  TEXT NOT NULL,
			actor       TEXT NOT NULL DEFAULT '',
			source      TEXT NOT NULL DEFAULT '',
			summary     TEXT NOT NULL DEFAULT '',
			payload     TEXT NOT NULL DEFAULT '{}'
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return sqlDB, db.New(sqlDB)
}

func TestWriteAndVerify(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()

	w := NewWriter(queries)

	// Write 5 entries.
	for i := 0; i < 5; i++ {
		if err := w.Record(ctx, "test.event", "tester", "test", "entry", "{}"); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	// Verify chain is valid.
	result, err := Verify(ctx, queries)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid chain, got: %+v", result)
	}
	if result.EntryCount != 5 {
		t.Fatalf("expected 5 entries, got %d", result.EntryCount)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	w := NewWriter(queries)

	// Write 3 entries.
	for i := 0; i < 3; i++ {
		if err := w.Record(ctx, "test.event", "tester", "test", "entry", "{}"); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	// Tamper with entry #2's hash directly in the DB.
	_, err := sqlDB.ExecContext(ctx, "UPDATE audit_entries SET hash = 'tampered' WHERE id = 2")
	if err != nil {
		t.Fatalf("tamper UPDATE: %v", err)
	}

	// Verify should detect the tamper.
	result, err := Verify(ctx, queries)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.Valid {
		t.Fatal("expected broken chain, got valid")
	}
	if result.FirstBroken != 2 {
		t.Fatalf("expected first broken at entry 2, got %d", result.FirstBroken)
	}
}

func TestVerifyEmptyChain(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()

	result, err := Verify(ctx, queries)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid for empty chain, got: %+v", result)
	}
	if result.EntryCount != 0 {
		t.Fatalf("expected 0 entries, got %d", result.EntryCount)
	}
}

func TestRecordIfWriterNil(t *testing.T) {
	ctx := context.Background()

	// Should not panic when writer is nil.
	RecordIfWriter(nil, ctx, "test.event", "tester", "test", "should be no-op", "{}")
}
