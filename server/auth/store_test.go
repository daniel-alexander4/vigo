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
