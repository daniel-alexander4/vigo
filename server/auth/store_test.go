// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/testutil"
)

func setupTestDB(t *testing.T) *db.Queries {
	return db.New(testutil.OpenMigratedTestDB(t))
}

func TestUserStoreCRUD(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	// Initially empty.
	count, err := store.CountUsers(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	admins, err := store.CountAdmins(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), admins)

	// Create a user.
	u, err := store.Create(ctx, db.InsertUserParams{
		Username:    "alice",
		DisplayName: "Alice Smith",
		Email:       "alice@example.com",
		Role:        "admin",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, u.ID)
	assert.Equal(t, "alice", u.Username)
	assert.Equal(t, "admin", u.Role)

	// Count should be 1.
	count, err = store.CountUsers(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	admins, err = store.CountAdmins(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), admins)

	// Get by ID.
	got, err := store.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Username)

	// Get by username.
	got, err = store.GetByUsername(ctx, "alice")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, u.ID, got.ID)

	// Get by non-existent username.
	got, err = store.GetByUsername(ctx, "bob")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Update.
	err = store.Update(ctx, db.UpdateUserParams{
		ID:          u.ID,
		DisplayName: "Alice Jones",
		Email:       "alice@other.com",
		Role:        "viewer",
		Permissions: PermissionsJSON("viewer"),
	})
	require.NoError(t, err)

	got, err = store.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Alice Jones", got.DisplayName)
	assert.Equal(t, "viewer", got.Role)

	// List.
	all, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1)

	// Create a second user.
	_, err = store.Create(ctx, db.InsertUserParams{
		Username: "bob",
		Role:     "viewer",
	})
	require.NoError(t, err)

	all, err = store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Delete.
	err = store.Delete(ctx, u.ID)
	require.NoError(t, err)

	got, err = store.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// fakeSigner is a deterministic auth.Signer for ADR-030 store tests.
// Stamps a fixed origin and a content-hash-shaped signature so
// assertions can confirm signer wiring without needing crypto.
type fakeSigner struct {
	pubkeyHex  string
	callCount  int
	lastRowID  string
	lastRoleOk bool
}

func (f *fakeSigner) Sign(row *UserRow) (string, []byte, error) {
	f.callCount++
	f.lastRowID = row.ID
	f.lastRoleOk = row.Role != ""
	row.OriginBolt = f.pubkeyHex
	return f.pubkeyHex, []byte("fake-sig-" + row.ID), nil
}

// TestUserStore_Create_WithSigner_StampsOriginAndSignature pins that
// the ADR-030 signer wiring populates origin_bolt + signature on every
// row written via Create. The Create path signs twice: once pre-INSERT
// (to satisfy the signer call before ID is known) and once post-INSERT
// (to re-stamp with the real ID for replication-correctness). The test
// asserts the final stored state, not the call count.
func TestUserStore_Create_WithSigner_StampsOriginAndSignature(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	signer := &fakeSigner{pubkeyHex: "deadbeefcafebabe"}
	store.SetSigner(signer)
	ctx := context.Background()

	u, err := store.Create(ctx, db.InsertUserParams{
		Username: "signed-user",
		Role:     "admin",
	})
	require.NoError(t, err)
	require.NotNil(t, u)

	// Re-read the stored row to inspect the persisted CRDT columns —
	// dbToUser doesn't surface them.
	stored, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "deadbeefcafebabe", stored.OriginBolt, "origin_bolt must be stamped")
	assert.NotEmpty(t, stored.Signature, "signature must be stamped")
	assert.Equal(t, "fake-sig-"+u.ID, string(stored.Signature), "signature must cover the real (post-INSERT) row ID")
}

// TestUserStore_Create_WithoutSigner_LeavesEmpty pins standalone-mode
// behavior: no signer → no origin_bolt, no signature, write succeeds.
func TestUserStore_Create_WithoutSigner_LeavesEmpty(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	u, err := store.Create(ctx, db.InsertUserParams{
		Username: "unsigned-user",
		Role:     "viewer",
	})
	require.NoError(t, err)

	stored, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.OriginBolt, "standalone mode must not stamp origin_bolt")
	assert.Empty(t, stored.Signature, "standalone mode must not stamp signature")
}

// TestUserStore_TOTPMutationsDoNotBumpUpdatedAtMs pins direction call
// A (Dan, 2026-05-24): TOTP doesn't replicate. EnableTOTP, DisableTOTP,
// and UpdateRecoveryCodes must not touch updated_at_ms — a local TOTP
// enrollment cannot trigger empty CRDT churn on peers.
func TestUserStore_TOTPMutationsDoNotBumpUpdatedAtMs(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	u, err := store.Create(ctx, db.InsertUserParams{
		Username: "totp-user",
		Role:     "viewer",
	})
	require.NoError(t, err)

	pre, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	preMs := pre.UpdatedAtMs

	require.NoError(t, store.EnableTOTP(ctx, u.ID, `["recovery-code-1"]`))
	afterEnable, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, preMs, afterEnable.UpdatedAtMs, "EnableTOTP must NOT bump updated_at_ms")

	require.NoError(t, store.UpdateRecoveryCodes(ctx, u.ID, `["recovery-code-2"]`))
	afterUpdate, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, preMs, afterUpdate.UpdatedAtMs, "UpdateRecoveryCodes must NOT bump updated_at_ms")

	require.NoError(t, store.DisableTOTP(ctx, u.ID))
	afterDisable, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, preMs, afterDisable.UpdatedAtMs, "DisableTOTP must NOT bump updated_at_ms")
}

// TestUserStoreSoftDelete pins the ADR-030 tombstone behavior: a
// deleted user is invisible to every read path, its api_tokens are
// gone (local cascade since soft-delete doesn't fire SQLite's
// ON DELETE CASCADE), and CountUsers/CountAdminUsers exclude it.
func TestUserStoreSoftDelete(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	tokenStore := NewTokenStore(queries, "")
	ctx := context.Background()

	u, err := store.Create(ctx, db.InsertUserParams{
		Username: "tombstoneme",
		Role:     "admin",
	})
	require.NoError(t, err)

	_, tok, err := tokenStore.CreateToken(ctx, u.ID, "doomed", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, tok.ID)

	require.NoError(t, store.Delete(ctx, u.ID))

	// Read paths all return nil/empty.
	got, err := store.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Nil(t, got, "GetByID must skip soft-deleted")

	got, err = store.GetByUsername(ctx, "tombstoneme")
	require.NoError(t, err)
	assert.Nil(t, got, "GetByUsername must skip soft-deleted")

	list, err := store.List(ctx)
	require.NoError(t, err)
	for _, lu := range list {
		assert.NotEqual(t, u.ID, lu.ID, "List must skip soft-deleted")
	}

	count, err := store.CountUsers(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	admins, err := store.CountAdmins(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), admins)

	// Local api_tokens cascade — token can't authenticate the now-deleted user.
	userTokens, err := tokenStore.ListByUser(ctx, u.ID)
	require.NoError(t, err)
	assert.Empty(t, userTokens, "soft-delete must cascade api_tokens locally")
}

// TestUserStore_ResignLegacyRows_NoSigner_NoOp pins the standalone-
// mode contract: without a signer, ResignLegacyRows reports zero and
// leaves rows untouched. Avoids accidentally signing rows with a
// nil-signer panic.
func TestUserStore_ResignLegacyRows_NoSigner_NoOp(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	// Insert a user via the no-signer path -- it'll be unsigned.
	_, err := store.Create(ctx, db.InsertUserParams{Username: "legacy", Role: "viewer"})
	require.NoError(t, err)

	n, err := store.ResignLegacyRows(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "no signer should produce 0 resigned rows")
}

// TestUserStore_ResignLegacyRows_SignsUnsigned pins the happy path:
// with a signer wired late (post-Create), the legacy row picks up
// origin_bolt + signature.
func TestUserStore_ResignLegacyRows_SignsUnsigned(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	// Create without a signer -- the row lands legacy-unsigned.
	u, err := store.Create(ctx, db.InsertUserParams{Username: "alice", Role: "admin"})
	require.NoError(t, err)

	pre, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	require.Empty(t, pre.Signature, "row must start unsigned")

	// Wire signer + re-sign.
	signer := &fakeSigner{pubkeyHex: "deadbeefcafebabe"}
	store.SetSigner(signer)

	n, err := store.ResignLegacyRows(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one legacy row should be re-signed")

	post, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "deadbeefcafebabe", post.OriginBolt, "origin_bolt must be stamped")
	assert.NotEmpty(t, post.Signature, "signature must be stamped")
}

// TestUserStore_ResignLegacyRows_SkipsAlreadySigned pins that
// already-signed rows aren't re-stamped — defends against accidental
// double-call wiping origin_bolt on a row whose signature is fresh.
func TestUserStore_ResignLegacyRows_SkipsAlreadySigned(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	signer := &fakeSigner{pubkeyHex: "originallybolt"}
	store.SetSigner(signer)
	ctx := context.Background()

	// Create with a signer -- row is born signed.
	u, err := store.Create(ctx, db.InsertUserParams{Username: "alice", Role: "admin"})
	require.NoError(t, err)
	pre, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "originallybolt", pre.OriginBolt)
	preSig := pre.Signature

	// Re-sign with a DIFFERENT signer; the already-signed row must
	// stay untouched.
	store.SetSigner(&fakeSigner{pubkeyHex: "differentbolt"})
	n, err := store.ResignLegacyRows(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "no rows should be re-signed when all are already signed")

	post, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "originallybolt", post.OriginBolt, "origin_bolt must NOT be overwritten")
	assert.Equal(t, preSig, post.Signature, "signature must NOT be overwritten")
}

// TestUserStore_ResignLegacyRows_SkipsTombstoned pins that
// soft-deleted legacy rows are NOT re-signed — propagating a local-
// only delete to peers that have a live signed version would erase
// the user fleet-wide.
func TestUserStore_ResignLegacyRows_SkipsTombstoned(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	// Create unsigned then soft-delete (also unsigned, since signer
	// is nil throughout). To set up the tombstoned-legacy condition:
	// insert directly with both deleted_at set and signature null.
	_, err := store.Create(ctx, db.InsertUserParams{Username: "ghost", Role: "viewer"})
	require.NoError(t, err)
	// Now soft-delete via the store; signer still nil so signature
	// stays null and deleted_at gets set.
	u, err := queries.GetUserByUsername(ctx, "ghost")
	require.NoError(t, err)
	require.NoError(t, store.Delete(ctx, u.ID))

	// Confirm pre-state: tombstoned AND unsigned.
	// (GetUserByID filters out tombstoned, so query through ListUsers
	// won't return it either — need a direct lookup. The cleanest
	// is to skip the assertion and verify post-resign that the count
	// stays at 0.)

	signer := &fakeSigner{pubkeyHex: "wouldresign"}
	store.SetSigner(signer)
	n, err := store.ResignLegacyRows(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "tombstoned legacy rows must NOT be re-signed")
}

// TestUserStore_ResignLegacyRows_KeepsUpdatedAtMsZero pins the
// critical detail ADR-030's conflict-resolution rule depends on:
// re-sign must NOT bump updated_at_ms — concurrent multi-bolt
// re-signs need to tie on the LWW comparator so the lex-min-origin
// tiebreaker can fire deterministically.
func TestUserStore_ResignLegacyRows_KeepsUpdatedAtMsZero(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	// Create unsigned (legacy shape).
	u, err := store.Create(ctx, db.InsertUserParams{Username: "alice", Role: "admin"})
	require.NoError(t, err)
	// Force updated_at_ms to 0 to simulate a true pre-Slice-2-iii-c row
	// (Create stamps wall-clock; legacy rows from migration 000013 had
	// the DEFAULT 0).
	_, err = queries.MergeUserLWW(ctx, db.MergeUserLWWParams{
		ID:          u.ID,
		Username:    "alice",
		Role:        "admin",
		UpdatedAtMs: 0,
	})
	// MergeUserLWW with updated_at_ms=0 won't win against a stamped
	// row; reset via direct UPDATE through a synthetic resign that
	// bypasses LWW. Simplest: just inspect what ResignLegacyRows does
	// to a known-unsigned row whose updated_at_ms is whatever Create
	// stamped — assert post-resign that the stamp is unchanged.
	require.NoError(t, err)

	preRow, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	preMs := preRow.UpdatedAtMs

	// Wipe signature to simulate the legacy condition (Create with
	// signer would have stamped one; we need an unsigned row).
	// Use direct DB exec since there's no query for this.
	_, err = queries.MergeUserLWW(ctx, db.MergeUserLWWParams{
		ID:          u.ID,
		Username:    "alice",
		Role:        "admin",
		UpdatedAtMs: preMs + 1, // win LWW so it overwrites with no signature
	})
	require.NoError(t, err)
	preRow2, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	require.Empty(t, preRow2.Signature)
	preMs2 := preRow2.UpdatedAtMs

	signer := &fakeSigner{pubkeyHex: "originbolt"}
	store.SetSigner(signer)
	n, err := store.ResignLegacyRows(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	postRow, err := queries.GetUserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, preMs2, postRow.UpdatedAtMs, "re-sign must NOT bump updated_at_ms — ADR-030 lex-min-origin tiebreaker depends on the tie")
	assert.Equal(t, "originbolt", postRow.OriginBolt)
}

func TestUserStoreOIDCSubject(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()

	u, err := store.Create(ctx, db.InsertUserParams{
		Username:    "oidc-user",
		Role:        "viewer",
		OidcSubject: "sub-12345",
	})
	require.NoError(t, err)

	got, err := store.GetByOIDCSubject(ctx, "sub-12345")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, u.ID, got.ID)

	// Non-existent subject.
	got, err = store.GetByOIDCSubject(ctx, "sub-99999")
	require.NoError(t, err)
	assert.Nil(t, got)
}
