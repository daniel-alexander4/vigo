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
	result, err := VerifyFull(ctx, queries)
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
	result, err := VerifyFull(ctx, queries)
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

	result, err := VerifyFull(ctx, queries)
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

func recordN(t *testing.T, w *Writer, ctx context.Context, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.Record(ctx, "test.event", "tester", "test", "entry", "{}"); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
}

func TestVerifier_IncrementalAdvancesCheckpoint(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()
	w := NewWriter(queries)
	recordN(t, w, ctx, 3)

	v := NewVerifier(queries)
	res, err := v.Verify(ctx) // cold → full walk
	if err != nil {
		t.Fatalf("cold Verify: %v", err)
	}
	if !res.Valid || res.EntryCount != 3 {
		t.Fatalf("cold Verify = %+v, want valid count 3", res)
	}

	recordN(t, w, ctx, 2) // append 2 more
	res, err = v.Verify(ctx)
	if err != nil {
		t.Fatalf("incremental Verify: %v", err)
	}
	if !res.Valid || res.EntryCount != 5 {
		t.Fatalf("incremental Verify = %+v, want valid count 5", res)
	}
}

func TestVerifier_IncrementalSkipsPreCheckpoint_FullCatches(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	w := NewWriter(queries)
	recordN(t, w, ctx, 3)

	v := NewVerifier(queries)
	if res, _ := v.Verify(ctx); !res.Valid { // anchors checkpoint at id 3
		t.Fatal("expected valid before tamper")
	}

	// Tamper an entry BELOW the checkpoint.
	if _, err := sqlDB.ExecContext(ctx, "UPDATE audit_entries SET summary='tampered' WHERE id=2"); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	// Incremental walks only id>3 (none) → does NOT catch the pre-checkpoint
	// tamper. This is the documented trade-off.
	if res, _ := v.Verify(ctx); !res.Valid {
		t.Fatalf("incremental Verify should not catch pre-checkpoint tamper, got %+v", res)
	}
	// VerifyFull re-walks everything and catches it.
	res, err := v.VerifyFull(ctx)
	if err != nil {
		t.Fatalf("VerifyFull: %v", err)
	}
	if res.Valid || res.FirstBroken != 2 {
		t.Fatalf("VerifyFull = %+v, want invalid first_broken 2", res)
	}
}

func TestVerifier_CatchesTamperAfterCheckpoint(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	w := NewWriter(queries)
	recordN(t, w, ctx, 2)

	v := NewVerifier(queries)
	v.Verify(ctx) // checkpoint at id 2

	recordN(t, w, ctx, 1) // id 3, after the checkpoint
	if _, err := sqlDB.ExecContext(ctx, "UPDATE audit_entries SET summary='tampered' WHERE id=3"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	res, err := v.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Valid || res.FirstBroken != 3 {
		t.Fatalf("incremental Verify = %+v, want invalid first_broken 3", res)
	}
}

func TestVerifier_ShrinkForcesFullWalk(t *testing.T) {
	// A deletion that shrinks the log below the checkpoint count must force a
	// full re-walk — so a tamper hidden behind the checkpoint can't be masked
	// by also deleting a later row.
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	w := NewWriter(queries)
	recordN(t, w, ctx, 4)

	v := NewVerifier(queries)
	v.Verify(ctx) // checkpoint at id 4, count 4

	if _, err := sqlDB.ExecContext(ctx, "UPDATE audit_entries SET summary='tampered' WHERE id=2"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "DELETE FROM audit_entries WHERE id=4"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, err := v.Verify(ctx) // count 3 < checkpoint count 4 → full walk
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Valid || res.FirstBroken != 2 {
		t.Fatalf("shrink should force a full walk catching the id-2 tamper, got %+v", res)
	}
}
