// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"testing"

	"git.alexander4.io/vigo/server/testutil"
)

// pubA / pubB are stable test pubkeys; values are not cryptographically
// meaningful, just unique strings the merge logic compares as TEXT.
const (
	pubA = "ed25519:0000000000000000000000000000000000000000000000000000000000000001"
	pubB = "ed25519:0000000000000000000000000000000000000000000000000000000000000002"
)

func openSpannerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d := testutil.OpenMigratedTestDB(t)
	// FK enforcement is per-connection in SQLite and not enabled by the
	// migrate harness; flip it on so the ConsumeToken FK to spanner_bolts
	// is actually exercised under test.
	if _, err := d.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign_keys: %v", err)
	}
	return d
}

func insertParams(pubkey string, patterns string, revokedAt sql.NullInt64, updatedAt int64) InsertBoltParams {
	return InsertBoltParams{
		Pubkey:    pubkey,
		SpannerID: "test-spanner",
		Hostname:  "bolt-" + pubkey[8:12],
		Patterns:  patterns,
		RevokedAt: revokedAt,
		RevokedBy: sql.NullString{},
		UpdatedAt: updatedAt,
		UpdatedBy: pubA,
		Signature: []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

func mergeParams(pubkey string, patterns string, revokedAt sql.NullInt64, updatedAt int64) MergeBoltLWWParams {
	return MergeBoltLWWParams{
		Pubkey:    pubkey,
		SpannerID: "test-spanner",
		Hostname:  "bolt-" + pubkey[8:12],
		Patterns:  patterns,
		RevokedAt: revokedAt,
		RevokedBy: sql.NullString{},
		UpdatedAt: updatedAt,
		UpdatedBy: pubA,
		Signature: []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

func TestInsertAndGetBolt(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	if err := q.InsertBolt(ctx, insertParams(pubA, `["*.us-west.*"]`, sql.NullInt64{}, 1000)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := q.GetBolt(ctx, pubA)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Pubkey != pubA || got.SpannerID != "test-spanner" || got.Patterns != `["*.us-west.*"]` || got.UpdatedAt != 1000 {
		t.Errorf("unexpected row: %+v", got)
	}
	if got.RevokedAt.Valid {
		t.Errorf("expected revoked_at NULL, got %d", got.RevokedAt.Int64)
	}
}

func TestListAndLiveBolts(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(q.InsertBolt(ctx, insertParams(pubA, `["a"]`, sql.NullInt64{}, 100)))
	must(q.InsertBolt(ctx, insertParams(pubB, `["b"]`, sql.NullInt64{Int64: 200, Valid: true}, 200)))

	all, err := q.ListBolts(ctx, "test-spanner")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListBolts: want 2, got %d", len(all))
	}

	live, err := q.ListLiveBolts(ctx, "test-spanner")
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 1 || live[0].Pubkey != pubA {
		t.Errorf("ListLiveBolts: want [%s], got %+v", pubA, live)
	}
}

// LWW arbitration table-test: newer wins, equal-with-revoke wins, and
// revoke is absorbing (no return to live regardless of timestamp).
func TestMergeBoltLWW(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	cases := []struct {
		name        string
		existing    MergeBoltLWWParams
		incoming    MergeBoltLWWParams
		wantMutated bool
		wantPattern string
	}{
		{
			name:        "newer_wins",
			existing:    mergeParams(pubA, `["old"]`, sql.NullInt64{}, 100),
			incoming:    mergeParams(pubA, `["new"]`, sql.NullInt64{}, 200),
			wantMutated: true,
			wantPattern: `["new"]`,
		},
		{
			name:        "older_loses",
			existing:    mergeParams(pubA, `["keep"]`, sql.NullInt64{}, 200),
			incoming:    mergeParams(pubA, `["stale"]`, sql.NullInt64{}, 100),
			wantMutated: false,
			wantPattern: `["keep"]`,
		},
		{
			name:        "equal_timestamp_revoke_wins_over_live",
			existing:    mergeParams(pubA, `["live"]`, sql.NullInt64{}, 100),
			incoming:    mergeParams(pubA, `["revoked"]`, sql.NullInt64{Int64: 100, Valid: true}, 100),
			wantMutated: true,
			wantPattern: `["revoked"]`,
		},
		{
			name:        "revoke_is_absorbing_no_unrevoke_via_newer",
			existing:    mergeParams(pubA, `["revoked"]`, sql.NullInt64{Int64: 100, Valid: true}, 100),
			incoming:    mergeParams(pubA, `["unrevoke"]`, sql.NullInt64{}, 200),
			wantMutated: false,
			wantPattern: `["revoked"]`,
		},
		{
			name:        "revoked_to_revoked_newer_updates_metadata",
			existing:    mergeParams(pubA, `["r1"]`, sql.NullInt64{Int64: 100, Valid: true}, 100),
			incoming:    mergeParams(pubA, `["r2"]`, sql.NullInt64{Int64: 200, Valid: true}, 200),
			wantMutated: true,
			wantPattern: `["r2"]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sqlDB.Exec(`DELETE FROM spanner_bolts`); err != nil {
				t.Fatal(err)
			}
			// Seed via the merge itself (an INSERT-only path); first call must
			// land since there's no conflicting row.
			seeded, err := q.MergeBoltLWW(ctx, tc.existing)
			if err != nil || seeded != 1 {
				t.Fatalf("seed: %v rows=%d", err, seeded)
			}
			got, err := q.MergeBoltLWW(ctx, tc.incoming)
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			mutated := got > 0
			if mutated != tc.wantMutated {
				t.Errorf("mutated=%v rows=%d, want %v", mutated, got, tc.wantMutated)
			}
			row, err := q.GetBolt(ctx, pubA)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if row.Patterns != tc.wantPattern {
				t.Errorf("patterns=%q, want %q", row.Patterns, tc.wantPattern)
			}
		})
	}
}

func TestConsumeTokenAndIsConsumed(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	tokenHash := "deadbeef" + "00000000000000000000000000000000000000000000000000000000"

	// Token starts unconsumed.
	consumed, err := q.IsTokenConsumed(ctx, tokenHash)
	if err != nil {
		t.Fatalf("is consumed (pre): %v", err)
	}
	if consumed != 0 {
		t.Errorf("token reported consumed before insert, got %d", consumed)
	}

	// Admission must land first (FK target).
	if err := q.InsertBolt(ctx, insertParams(pubA, `["x"]`, sql.NullInt64{}, 1)); err != nil {
		t.Fatalf("insert admission: %v", err)
	}
	if err := q.ConsumeToken(ctx, ConsumeTokenParams{
		TokenHash:            tokenHash,
		SpannerID:            "test-spanner",
		ConsumedAt:           42,
		ConsumedByBoltPubkey: pubA,
		Signature:            []byte{0x01},
	}); err != nil {
		t.Fatalf("consume: %v", err)
	}
	consumed, err = q.IsTokenConsumed(ctx, tokenHash)
	if err != nil {
		t.Fatalf("is consumed (post): %v", err)
	}
	if consumed != 1 {
		t.Errorf("token not reported consumed after insert, got %d", consumed)
	}

	// Re-consume same token: PK collision.
	if err := q.ConsumeToken(ctx, ConsumeTokenParams{
		TokenHash:            tokenHash,
		SpannerID:            "test-spanner",
		ConsumedAt:           43,
		ConsumedByBoltPubkey: pubA,
		Signature:            []byte{0x02},
	}); err == nil {
		t.Error("expected PK conflict on second consume, got nil")
	}
}

func TestConsumeTokenFKEnforced(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	err := q.ConsumeToken(ctx, ConsumeTokenParams{
		TokenHash:            "abcd",
		SpannerID:            "test-spanner",
		ConsumedAt:           1,
		ConsumedByBoltPubkey: pubA, // no admission row exists
		Signature:            []byte{0x01},
	})
	if err == nil {
		t.Fatal("expected FK violation on consume without admission, got nil")
	}
}

// Atomic admission+consume must land both or neither — rollback semantics
// when the consume hits a duplicate.
func TestAdmissionAndConsumeAtomicity(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	// Pre-consume a token so the second attempt collides.
	if err := q.InsertBolt(ctx, insertParams(pubA, `["seed"]`, sql.NullInt64{}, 1)); err != nil {
		t.Fatalf("seed bolt: %v", err)
	}
	if err := q.ConsumeToken(ctx, ConsumeTokenParams{
		TokenHash:            "dup-token",
		SpannerID:            "test-spanner",
		ConsumedAt:           1,
		ConsumedByBoltPubkey: pubA,
		Signature:            []byte{0x01},
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	// Attempt to admit pubB and consume the already-consumed token in one tx.
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	qtx := q.WithTx(tx)
	if err := qtx.InsertBolt(ctx, insertParams(pubB, `["new"]`, sql.NullInt64{}, 2)); err != nil {
		t.Fatalf("tx insert: %v", err)
	}
	consumeErr := qtx.ConsumeToken(ctx, ConsumeTokenParams{
		TokenHash:            "dup-token",
		SpannerID:            "test-spanner",
		ConsumedAt:           2,
		ConsumedByBoltPubkey: pubB,
		Signature:            []byte{0x02},
	})
	if consumeErr == nil {
		t.Fatal("expected consume PK collision; got nil")
		tx.Rollback()
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// After rollback, pubB must not appear in the roster.
	if _, err := q.GetBolt(ctx, pubB); err != sql.ErrNoRows {
		t.Errorf("pubB landed despite rollback; got err=%v", err)
	}
}
