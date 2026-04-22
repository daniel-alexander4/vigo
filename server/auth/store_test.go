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
