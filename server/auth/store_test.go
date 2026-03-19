// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hobnob/hobnob/server/db"
)

func setupTestDB(t *testing.T) *db.Queries {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			username TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('admin', 'viewer')),
			permissions TEXT NOT NULL DEFAULT '',
			ssh_public_key TEXT NOT NULL DEFAULT '',
			oidc_subject TEXT NOT NULL DEFAULT '',
			totp_enabled INTEGER NOT NULL DEFAULT 0,
			totp_recovery TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE api_tokens (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT '',
			token_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			last_used_at DATETIME,
			expires_at DATETIME
		);
	`)
	require.NoError(t, err)
	return db.New(sqlDB)
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
