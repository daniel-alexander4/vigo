// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// signedUserRow builds a UserSnapshotRow signed by the given key.
// Used by the merge tests to mint inputs the verify-then-merge path
// will accept (or, with a tampered field, reject).
func signedUserRow(t *testing.T, signingKey ed25519.PrivateKey, originHex string, row vigoauth.UserRow) UserSnapshotRow {
	t.Helper()
	row.OriginBolt = originHex
	sig, err := SignUserRow(signingKey, &row)
	if err != nil {
		t.Fatalf("sign user row: %v", err)
	}
	return UserSnapshotRow{UserRow: row, Signature: sig}
}

// boltMap builds the per-pubkey known-bolt map MergeUsers consumes.
// The fixture's founder is the default origin; tests that need
// additional bolts add them explicitly.
func (f *crdtFixture) boltMap() map[string]knownBolt {
	t := f.t
	t.Helper()
	return map[string]knownBolt{
		f.founderHex: {pubkey: f.founderPub, revoked: false},
	}
}

// seedUser inserts a baseline user row directly via the merge path
// (so the row gets origin_bolt + signature set, making it visible to
// pre-merge GetUserByID in subsequent merges). Returns the row's id.
func (f *crdtFixture) seedUser(t *testing.T, username, role string, updatedAtMs int64) string {
	t.Helper()
	id := "user-" + username
	row := vigoauth.UserRow{
		ID:          id,
		Username:    username,
		Role:        role,
		Permissions: "",
		UpdatedAtMs: updatedAtMs,
	}
	sr := signedUserRow(t, f.founderKey, f.founderHex, row)
	effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("seedUser %s: %v", username, err)
	}
	// Brand-new user: no pre row, no effect emitted (only role-change/
	// tombstone produce effects).
	if len(effects) != 0 {
		t.Fatalf("seedUser %s: unexpected effects: %v", username, effects)
	}
	return id
}

// TestMergeUsers_AcceptsValidSignedRow pins the happy path: a fresh
// user from a known live bolt with a valid signature lands in the
// table, no effect emitted (it's a new row, not a role-change).
func TestMergeUsers_AcceptsValidSignedRow(t *testing.T) {
	f := newCRDTFixture(t)
	row := vigoauth.UserRow{
		ID:          "user-alice",
		Username:    "alice",
		Role:        "admin",
		UpdatedAtMs: time.Now().UnixMilli(),
	}
	sr := signedUserRow(t, f.founderKey, f.founderHex, row)

	effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if len(effects) != 0 {
		t.Errorf("fresh-row merge should emit no effect, got %v", effects)
	}
	got, err := f.q.GetUserByID(context.Background(), "user-alice")
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Role != "admin" || got.OriginBolt != f.founderHex {
		t.Errorf("stored row mismatch: role=%s origin=%s", got.Role, got.OriginBolt)
	}
}

// TestMergeUsers_RejectsInvalidSignature pins the security boundary:
// a row whose signature doesn't verify against the named origin's
// pubkey is dropped — never reaches the DB.
func TestMergeUsers_RejectsInvalidSignature(t *testing.T) {
	f := newCRDTFixture(t)
	row := vigoauth.UserRow{
		ID:          "user-mallory",
		Username:    "mallory",
		Role:        "admin",
		UpdatedAtMs: time.Now().UnixMilli(),
	}
	sr := signedUserRow(t, f.founderKey, f.founderHex, row)
	// Tamper the role AFTER signing — verify must reject.
	sr.Role = "viewer"

	effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if len(effects) != 0 {
		t.Errorf("tampered row should not emit effect, got %v", effects)
	}
	if _, err := f.q.GetUserByID(context.Background(), "user-mallory"); err == nil {
		t.Error("tampered row reached the DB")
	}
}

// TestMergeUsers_RejectsReservedPrincipal pins that a validly-signed row
// from a known origin is still dropped if it claims a reserved synthetic
// identifier — by id (the token-owner orphan-guard vector) or by username.
// A guarded peer can't originate one; this defends against a forged/stale
// origin.
func TestMergeUsers_RejectsReservedPrincipal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		id       string
		username string
	}{
		{"reserved id", vigoauth.LocalAdminUserID, "someuser"},
		{"reserved username", "user-localhost", vigoauth.LocalAdminUserID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCRDTFixture(t)
			row := vigoauth.UserRow{
				ID:          tc.id,
				Username:    tc.username,
				Role:        "admin",
				UpdatedAtMs: time.Now().UnixMilli(),
			}
			sr := signedUserRow(t, f.founderKey, f.founderHex, row)

			effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
			if err != nil {
				t.Fatalf("MergeUsers: %v", err)
			}
			if len(effects) != 0 {
				t.Errorf("reserved-principal row should not emit effect, got %v", effects)
			}
			if _, err := f.q.GetUserByID(context.Background(), tc.id); err == nil {
				t.Error("reserved-principal row reached the DB")
			}
		})
	}
}

// TestMergeUsers_RejectsUnknownOrigin pins forge-resistance: a row
// whose origin_bolt is not in the local roster is dropped, no
// matter how well-signed (signed by an unknown party).
func TestMergeUsers_RejectsUnknownOrigin(t *testing.T) {
	f := newCRDTFixture(t)
	_, foreignKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	row := vigoauth.UserRow{
		ID:          "user-forged",
		Username:    "forged",
		Role:        "admin",
		UpdatedAtMs: time.Now().UnixMilli(),
	}
	foreignHex := hex.EncodeToString(make([]byte, 32)) // not in roster
	sr := signedUserRow(t, foreignKey, foreignHex, row)

	effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if len(effects) != 0 {
		t.Errorf("unknown-origin row should not emit effect, got %v", effects)
	}
	if _, err := f.q.GetUserByID(context.Background(), "user-forged"); err == nil {
		t.Error("unknown-origin row reached the DB")
	}
}

// TestMergeUsers_RoleChange_TriggersEffect pins the
// destroy-sessions trigger: a merge that changes role emits an
// effect with RoleChanged=true, which the post-commit dispatcher
// turns into DestroyUserSessions + audit.
func TestMergeUsers_RoleChange_TriggersEffect(t *testing.T) {
	f := newCRDTFixture(t)
	id := f.seedUser(t, "alice", "admin", 1000)

	demoted := vigoauth.UserRow{
		ID:          id,
		Username:    "alice",
		Role:        "viewer",
		UpdatedAtMs: 2000, // newer, wins LWW
	}
	sr := signedUserRow(t, f.founderKey, f.founderHex, demoted)
	effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %v", effects)
	}
	if !effects[0].RoleChanged {
		t.Errorf("RoleChanged should be true, got %+v", effects[0])
	}
	if effects[0].Tombstoned {
		t.Errorf("Tombstoned should be false (not a delete), got %+v", effects[0])
	}
}

// TestMergeUsers_Tombstone_TriggersEffectAndApiTokenCascade pins
// the soft-delete propagation: a fresh tombstone produces a
// Tombstoned effect AND atomically deletes the user's local
// api_tokens (replacing the FK cascade that doesn't fire on UPDATE).
func TestMergeUsers_Tombstone_TriggersEffectAndApiTokenCascade(t *testing.T) {
	f := newCRDTFixture(t)
	id := f.seedUser(t, "alice", "viewer", 1000)

	// Seed an api_token for alice.
	ctx := context.Background()
	if _, err := f.q.InsertAPIToken(ctx, db.InsertAPITokenParams{
		UserID:    id,
		Name:      "doomed",
		TokenHash: "sha256-doomed",
	}); err != nil {
		t.Fatalf("InsertAPIToken: %v", err)
	}

	tombstoned := vigoauth.UserRow{
		ID:          id,
		Username:    "alice",
		Role:        "viewer",
		UpdatedAtMs: 2000,
		DeletedAt:   2000,
	}
	sr := signedUserRow(t, f.founderKey, f.founderHex, tombstoned)
	effects, err := MergeUsers(ctx, f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if len(effects) != 1 || !effects[0].Tombstoned {
		t.Fatalf("expected Tombstoned=true effect, got %v", effects)
	}

	// api_tokens for alice must be gone (in-tx cascade).
	tokens, err := f.q.ListAPITokensByUser(ctx, id)
	if err != nil {
		t.Fatalf("ListAPITokensByUser: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected api_tokens cascade on tombstone, got %d tokens", len(tokens))
	}
}

// TestMergeUsers_BenignChange_NoEffect pins that non-security
// changes (display_name, email) merge silently — no DestroyUserSessions,
// no audit event noise.
func TestMergeUsers_BenignChange_NoEffect(t *testing.T) {
	f := newCRDTFixture(t)
	id := f.seedUser(t, "alice", "viewer", 1000)

	renamed := vigoauth.UserRow{
		ID:          id,
		Username:    "alice",
		DisplayName: "Alice Renamed",
		Email:       "alice@new.example.com",
		Role:        "viewer",
		UpdatedAtMs: 2000,
	}
	sr := signedUserRow(t, f.founderKey, f.founderHex, renamed)
	effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if len(effects) != 0 {
		t.Errorf("benign change should emit no effect, got %v", effects)
	}
	// Confirm the merge actually applied the change.
	got, err := f.q.GetUserByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.DisplayName != "Alice Renamed" {
		t.Errorf("display name not updated: %q", got.DisplayName)
	}
}

// TestMergeUsers_LWWLoses_NoEffect pins that an older incoming
// row loses to the local newer one: no merge, no effect.
func TestMergeUsers_LWWLoses_NoEffect(t *testing.T) {
	f := newCRDTFixture(t)
	id := f.seedUser(t, "alice", "admin", 5000)

	older := vigoauth.UserRow{
		ID:          id,
		Username:    "alice",
		Role:        "viewer", // role flip would normally trigger
		UpdatedAtMs: 1000,     // older than seeded 5000 → LWW loses
	}
	sr := signedUserRow(t, f.founderKey, f.founderHex, older)
	effects, err := MergeUsers(context.Background(), f.q, []UserSnapshotRow{sr}, f.boltMap())
	if err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if len(effects) != 0 {
		t.Errorf("LWW-losing row should emit no effect, got %v", effects)
	}
	// Local row unchanged.
	got, err := f.q.GetUserByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Role != "admin" {
		t.Errorf("local role overwritten despite LWW loss: %q", got.Role)
	}
}
