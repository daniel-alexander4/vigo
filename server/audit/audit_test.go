// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"context"
	"database/sql"
	"testing"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/testutil"
)

func setupTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	sqlDB := testutil.OpenMigratedTestDB(t)
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

func TestRecordInvokesOnRecord(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()

	w := NewWriter(queries)

	var got struct {
		eventType, actor, source, summary, payload string
		calls                                      int
	}
	w.OnRecord = func(eventType, actor, source, summary, payload string) {
		got.eventType = eventType
		got.actor = actor
		got.source = source
		got.summary = summary
		got.payload = payload
		got.calls++
	}

	if err := w.Record(ctx, "config.published", "dan", "cli", "stack v42", `{"version":42}`); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if got.calls != 1 {
		t.Fatalf("OnRecord called %d times, want 1", got.calls)
	}
	if got.eventType != "config.published" || got.actor != "dan" || got.source != "cli" ||
		got.summary != "stack v42" || got.payload != `{"version":42}` {
		t.Fatalf("OnRecord got mismatched args: %+v", got)
	}
}

func TestRecordOnRecordNilTolerant(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()

	// Writer with no OnRecord must not panic.
	w := NewWriter(queries)
	if err := w.Record(ctx, "test.event", "tester", "test", "no callback", "{}"); err != nil {
		t.Fatalf("Record: %v", err)
	}
}
