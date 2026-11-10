// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"git.alexander4.io/vigo/server/db"
)

// failFirstRowDB wraps a DBTX and runs its first n QueryRowContext calls under
// an already-cancelled context, simulating a wedged boot-time head lookup.
type failFirstRowDB struct {
	db.DBTX
	n int
}

func (f *failFirstRowDB) QueryRowContext(ctx context.Context, q string, args ...interface{}) *sql.Row {
	if f.n > 0 {
		f.n--
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		return f.DBTX.QueryRowContext(cctx, q, args...)
	}
	return f.DBTX.QueryRowContext(ctx, q, args...)
}

// A head lookup that fails at boot must not start a second chain: the first
// Record re-reads the head, the in-band marker links to it, and the whole log
// still verifies.
func TestHeadLoadFailureDoesNotForkChain(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	w := NewWriter(queries)
	for i := 0; i < 3; i++ {
		if err := w.Record(ctx, "test.event", "tester", "test", "entry", "{}"); err != nil {
			t.Fatal(err)
		}
	}

	flaky := db.New(&failFirstRowDB{DBTX: sqlDB, n: 1})
	w2 := NewWriter(flaky)
	if err := w2.Record(ctx, "after.reboot", "tester", "test", "entry", "{}"); err != nil {
		t.Fatalf("Record after deferred head load: %v", err)
	}

	res, err := VerifyFull(ctx, queries)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.EntryCount != 5 {
		t.Fatalf("chain must stay valid across a deferred head load, got %+v", res)
	}
	marker, err := queries.GetAuditEntry(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if marker.EventType != "audit.head_load_deferred" {
		t.Fatalf("entry 4 = %q, want the in-band head_load_deferred marker", marker.EventType)
	}
	prev, _ := queries.GetAuditEntry(ctx, 3)
	if marker.PrevHash != prev.Hash {
		t.Fatalf("marker prev_hash %q must link to entry 3 hash %q", marker.PrevHash, prev.Hash)
	}
}

// A row recorded under a hash_version this build does not know is refused,
// never verified with a guessed scheme.
func TestVerifyRejectsUnknownHashVersion(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	w := NewWriter(queries)
	if err := w.Record(ctx, "test.event", "tester", "test", "entry", "{}"); err != nil {
		t.Fatal(err)
	}
	// Re-hash entry 1 with V1 and label it version 9: a V1 fallback would
	// call this clean.
	e, _ := queries.GetAuditEntry(ctx, 1)
	v1 := computeHashV1("", e.Timestamp.UTC().Format(timestampFormat), e.EventType, e.Actor, e.Source, e.Summary, e.Payload)
	if _, err := sqlDB.ExecContext(ctx, "UPDATE audit_entries SET hash = ?, hash_version = 9 WHERE id = 1", v1); err != nil {
		t.Fatal(err)
	}
	anchors.Delete(queries) // isolate the hash_version check from the head anchor
	res, err := VerifyFull(ctx, queries)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || res.FirstBroken != 1 {
		t.Fatalf("unknown hash_version must be refused, got %+v", res)
	}
}

// Deleting the newest entries leaves an internally consistent chain; only the
// head anchor can see it.
func TestVerifyDetectsTailTruncation(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	w := NewWriter(queries)
	for i := 0; i < 4; i++ {
		if err := w.Record(ctx, "test.event", "tester", "test", "entry", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sqlDB.ExecContext(ctx, "DELETE FROM audit_entries WHERE id >= 3"); err != nil {
		t.Fatal(err)
	}
	res, err := VerifyFull(ctx, queries)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatalf("tail truncation must be detected, got %+v", res)
	}
}

// Across a restart the anchor comes from the head file: a truncated DB is
// reported until the Writer records the mismatch in-band, after which the
// chain verifies again and the marker names the lost head.
func TestHeadFileDetectsTruncationAcrossRestart(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()
	headPath := filepath.Join(t.TempDir(), HeadAnchorFile)

	w := NewWriter(queries, WithHeadFile(headPath))
	for i := 0; i < 4; i++ {
		if err := w.Record(ctx, "test.event", "tester", "test", "entry", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	// Clean restart: no marker, still valid.
	anchors.Delete(queries)
	w = NewWriter(queries, WithHeadFile(headPath))
	if w.pendingMarkerType != "" {
		t.Fatalf("clean restart must not queue a marker, got %q", w.pendingMarkerType)
	}

	if _, err := sqlDB.ExecContext(ctx, "DELETE FROM audit_entries WHERE id >= 3"); err != nil {
		t.Fatal(err)
	}
	anchors.Delete(queries) // a fresh process: only the file remembers the head
	w = NewWriter(queries, WithHeadFile(headPath))
	res, err := VerifyFull(ctx, queries)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatalf("truncation before restart must be detected from the head file, got %+v", res)
	}
	if err := w.Record(ctx, "after.restart", "tester", "test", "entry", "{}"); err != nil {
		t.Fatal(err)
	}
	last, err := queries.GetLastAuditEntry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := queries.GetAuditEntry(ctx, last.ID-1)
	if m.EventType != "audit.head_mismatch" {
		t.Fatalf("expected in-band head_mismatch marker before the event, got %q", m.EventType)
	}
	if res, _ := VerifyFull(ctx, queries); !res.Valid {
		t.Fatalf("after the in-band marker the chain verifies again, got %+v", res)
	}
}
