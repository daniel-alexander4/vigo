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
	// migrate harness; flip it on so the spanner_bolts.consumed_token_hash
	// FK to spanner_consumed_tokens is actually exercised under test.
	if _, err := d.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign_keys: %v", err)
	}
	return d
}

// boltParams builds an admission row with no consumed-token reference (a
// pre-trusted entry). admittedAt doubles as the LWW arbitration clock.
func boltParams(pubkey, patternClaims string, revokedAt sql.NullInt64, admittedAt int64) InsertBoltParams {
	return InsertBoltParams{
		Pubkey:            pubkey,
		Hostname:          "bolt-" + pubkey[len(pubkey)-4:],
		AdmittedAt:        admittedAt,
		AdmittedBy:        pubA,
		ConsumedTokenHash: sql.NullString{},
		RevokedAt:         revokedAt,
		RevokedBy:         sql.NullString{},
		LastSnapshotAt:    sql.NullInt64{},
		PatternClaims:     patternClaims,
		Signature:         []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

// mergeParams is boltParams shaped for the gossip-replication entry point;
// the two query param structs share an identical column list.
func mergeParams(pubkey, patternClaims string, revokedAt sql.NullInt64, admittedAt int64) MergeBoltLWWParams {
	return MergeBoltLWWParams(boltParams(pubkey, patternClaims, revokedAt, admittedAt))
}

func tokenParams(tokenHash string, consumedAt int64) ConsumeTokenParams {
	return ConsumeTokenParams{
		TokenHash:      tokenHash,
		ConsumedAt:     consumedAt,
		ConsumedBy:     pubA,
		AdmittedPubkey: pubB,
		ExpiresAt:      consumedAt + 86_400_000, // +24h
	}
}

func TestInsertAndGetBolt(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	if err := q.InsertBolt(ctx, boltParams(pubA, `["*.us-west.*"]`, sql.NullInt64{}, 1000)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := q.GetBolt(ctx, pubA)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Pubkey != pubA || got.PatternClaims != `["*.us-west.*"]` || got.AdmittedAt != 1000 {
		t.Errorf("unexpected row: %+v", got)
	}
	if got.RevokedAt.Valid {
		t.Errorf("expected revoked_at NULL, got %d", got.RevokedAt.Int64)
	}
	if got.ConsumedTokenHash.Valid {
		t.Errorf("expected consumed_token_hash NULL for a pre-trusted entry")
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
	must(q.InsertBolt(ctx, boltParams(pubA, `["a"]`, sql.NullInt64{}, 100)))
	must(q.InsertBolt(ctx, boltParams(pubB, `["b"]`, sql.NullInt64{Int64: 200, Valid: true}, 200)))

	all, err := q.ListBolts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListBolts: want 2, got %d", len(all))
	}

	live, err := q.ListLiveBolts(ctx)
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 1 || live[0].Pubkey != pubA {
		t.Errorf("ListLiveBolts: want [%s], got %+v", pubA, live)
	}
}

func TestRevokeBolt(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	if err := q.InsertBolt(ctx, boltParams(pubA, `["x"]`, sql.NullInt64{}, 100)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := q.RevokeBolt(ctx, RevokeBoltParams{
		Pubkey:    pubA,
		RevokedAt: sql.NullInt64{Int64: 500, Valid: true},
		RevokedBy: sql.NullString{String: pubB, Valid: true},
		Signature: []byte{0x01},
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, err := q.GetBolt(ctx, pubA)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.RevokedAt.Valid || got.RevokedAt.Int64 != 500 {
		t.Errorf("expected revoked_at=500, got %+v", got.RevokedAt)
	}
	live, err := q.ListLiveBolts(ctx)
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("ListLiveBolts: want 0 after revoke, got %d", len(live))
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
			if row.PatternClaims != tc.wantPattern {
				t.Errorf("pattern_claims=%q, want %q", row.PatternClaims, tc.wantPattern)
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

	// The consumed-tokens G-Set has no FK — a token row stands alone.
	if err := q.ConsumeToken(ctx, tokenParams(tokenHash, 42)); err != nil {
		t.Fatalf("consume: %v", err)
	}
	consumed, err = q.IsTokenConsumed(ctx, tokenHash)
	if err != nil {
		t.Fatalf("is consumed (post): %v", err)
	}
	if consumed != 1 {
		t.Errorf("token not reported consumed after insert, got %d", consumed)
	}

	got, err := q.GetConsumedToken(ctx, tokenHash)
	if err != nil {
		t.Fatalf("get consumed token: %v", err)
	}
	if got.ConsumedBy != pubA || got.AdmittedPubkey != pubB {
		t.Errorf("unexpected consumed-token row: %+v", got)
	}

	// Re-consume the same token: token_hash PK collision — one-shot.
	if err := q.ConsumeToken(ctx, tokenParams(tokenHash, 43)); err == nil {
		t.Error("expected PK conflict on second consume, got nil")
	}
}

// The FK runs spanner_bolts.consumed_token_hash -> spanner_consumed_tokens:
// an admission may only reference a join token whose row already exists.
func TestBoltConsumedTokenFKEnforced(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	bad := boltParams(pubA, `["x"]`, sql.NullInt64{}, 1)
	bad.ConsumedTokenHash = sql.NullString{String: "no-such-token", Valid: true}
	if err := q.InsertBolt(ctx, bad); err == nil {
		t.Fatal("expected FK violation admitting a bolt with an unknown token, got nil")
	}

	// With the token recorded first, the same admission satisfies the FK.
	if err := q.ConsumeToken(ctx, tokenParams("real-token", 1)); err != nil {
		t.Fatalf("consume token: %v", err)
	}
	ok := boltParams(pubA, `["x"]`, sql.NullInt64{}, 1)
	ok.ConsumedTokenHash = sql.NullString{String: "real-token", Valid: true}
	if err := q.InsertBolt(ctx, ok); err != nil {
		t.Fatalf("insert admission with valid token ref: %v", err)
	}
}

// Atomic admission+consume must land both or neither. The token INSERT is
// ordered first (it is the FK target); a rollback drops both.
func TestAdmissionAndConsumeAtomicity(t *testing.T) {
	sqlDB := openSpannerTestDB(t)
	q := New(sqlDB)
	ctx := context.Background()

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	qtx := q.WithTx(tx)
	if err := qtx.ConsumeToken(ctx, tokenParams("tok-1", 1)); err != nil {
		t.Fatalf("tx consume: %v", err)
	}
	admit := boltParams(pubB, `["new"]`, sql.NullInt64{}, 2)
	admit.ConsumedTokenHash = sql.NullString{String: "tok-1", Valid: true}
	if err := qtx.InsertBolt(ctx, admit); err != nil {
		t.Fatalf("tx insert: %v", err)
	}
	// A downstream failure rolls the whole tx back.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Neither the admission nor the token survived the rollback.
	if _, err := q.GetBolt(ctx, pubB); err != sql.ErrNoRows {
		t.Errorf("pubB landed despite rollback; got err=%v", err)
	}
	consumed, err := q.IsTokenConsumed(ctx, "tok-1")
	if err != nil {
		t.Fatalf("is consumed: %v", err)
	}
	if consumed != 0 {
		t.Errorf("token landed despite rollback, got %d", consumed)
	}
}
