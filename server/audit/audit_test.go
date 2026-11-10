// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

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

// insertV1Entry hand-writes a row using the legacy V1 (bare-concatenation)
// scheme and hash_version=1, simulating a row recorded before the version
// column existed. Returns the stored hash so the caller can chain the next.
func insertV1Entry(t *testing.T, sqlDB *sql.DB, ctx context.Context, prevHash, eventType, actor, source, summary, payload string) string {
	t.Helper()
	ts := time.Now().UTC().Truncate(time.Second)
	tsStr := ts.Format(timestampFormat)
	hash := computeHashV1(prevHash, tsStr, eventType, actor, source, summary, payload)
	_, err := sqlDB.ExecContext(ctx,
		`INSERT INTO audit_entries (prev_hash, hash, timestamp, event_type, actor, source, summary, payload, hash_version)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		prevHash, hash, ts, eventType, actor, source, summary, payload)
	if err != nil {
		t.Fatalf("insert V1 entry: %v", err)
	}
	return hash
}

func TestMixedV1V2ChainVerifies(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	// Pre-existing V1 rows (recorded before the format bump).
	prev := ""
	prev = insertV1Entry(t, sqlDB, ctx, prev, "legacy.event", "old", "boot", "first", "{}")
	prev = insertV1Entry(t, sqlDB, ctx, prev, "legacy.event", "old", "boot", "second", "{}")

	// New writer picks up the chain head and appends V2 rows.
	w := NewWriter(queries)
	if w.prevHash != prev {
		t.Fatalf("writer prevHash = %q, want %q (chain head from V1 rows)", w.prevHash, prev)
	}
	recordN(t, w, ctx, 3)

	res, err := VerifyFull(ctx, queries)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid mixed V1/V2 chain, got: %+v", res)
	}
	if res.EntryCount != 5 {
		t.Fatalf("expected 5 entries, got %d", res.EntryCount)
	}

	// Confirm the new rows were persisted as V2 and the old ones stayed V1.
	all, err := queries.ListAuditEntriesAsc(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range all {
		want := hashVersionV2
		if e.ID <= 2 {
			want = hashVersionV1
		}
		if e.HashVersion != want {
			t.Fatalf("entry %d hash_version = %d, want %d", e.ID, e.HashVersion, want)
		}
	}
}

func TestVerifyDetectsTamperOnV2Row(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	w := NewWriter(queries)
	recordN(t, w, ctx, 3) // all V2

	// Tamper a V2 row's field — the recomputed V2 hash must no longer match.
	if _, err := sqlDB.ExecContext(ctx, "UPDATE audit_entries SET summary='tampered' WHERE id=2"); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	res, err := VerifyFull(ctx, queries)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Valid {
		t.Fatal("expected broken chain after tampering a V2 row, got valid")
	}
	if res.FirstBroken != 2 {
		t.Fatalf("expected first broken at entry 2, got %d", res.FirstBroken)
	}
}

// TestV1HashByteIdentical guards the frozen invariant: V1 recompute must equal
// the original bare-concatenation digest. If anyone "improves" computeHashV1,
// every pre-existing audit row in the field stops verifying — this catches it.
func TestV1HashByteIdentical(t *testing.T) {
	h := sha256.New()
	for _, s := range []string{"prev", "2026-01-01T00:00:00Z", "evt", "actor", "src", "sum", "pay"} {
		h.Write([]byte(s))
	}
	want := hex.EncodeToString(h.Sum(nil))
	got := computeHashV1("prev", "2026-01-01T00:00:00Z", "evt", "actor", "src", "sum", "pay")
	if got != want {
		t.Fatalf("V1 hash drifted from frozen concatenation: got %s want %s", got, want)
	}
}

// TestV2DisambiguatesFieldBoundaries proves V2 fixes the V1 collision:
// (actor="ab",source="c") and (actor="a",source="bc") must hash differently.
func TestV2DisambiguatesFieldBoundaries(t *testing.T) {
	a := computeHashV2("p", "t", "e", "ab", "c", "s", "y")
	b := computeHashV2("p", "t", "e", "a", "bc", "s", "y")
	if a == b {
		t.Fatal("V2 hashes collide across a shifted field boundary")
	}
	// And confirm V1 actually does collide here (the bug V2 fixes).
	if computeHashV1("p", "t", "e", "ab", "c", "s", "y") != computeHashV1("p", "t", "e", "a", "bc", "s", "y") {
		t.Fatal("expected V1 to collide on shifted boundary (sanity check of the documented weakness)")
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
