// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAPIToken(t *testing.T) {
	plain, hash, err := GenerateAPIToken()
	require.NoError(t, err)

	// Token should have the prefix.
	assert.True(t, len(plain) > 5)
	assert.Equal(t, "vgot_", plain[:5])

	// Hash should be valid bcrypt.
	assert.True(t, ValidateAPIToken(plain, hash))

	// Different plaintext should not match.
	assert.False(t, ValidateAPIToken("vgot_wrong", hash))
}

func TestTokenCreateAndValidate(t *testing.T) {
	queries := setupTestDB(t)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries)
	ctx := context.Background()

	// Create a user first.
	u, err := userStore.Create(ctx, db.InsertUserParams{
		Username: "tokenuser",
		Role:     "admin",
	})
	require.NoError(t, err)

	// Create a token.
	plain, tok, err := tokenStore.CreateToken(ctx, u.ID, "my-token", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, plain)
	assert.NotEmpty(t, tok.ID)
	assert.Equal(t, "my-token", tok.Name)

	// Validate the token.
	resolved, err := tokenStore.ValidateAndResolve(ctx, plain, userStore)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, u.ID, resolved.ID)

	// Invalid token should return nil.
	resolved, err = tokenStore.ValidateAndResolve(ctx, "vgot_invalid", userStore)
	require.NoError(t, err)
	assert.Nil(t, resolved)

	// Non-vgot_ prefix should return nil.
	resolved, err = tokenStore.ValidateAndResolve(ctx, "not-a-token", userStore)
	require.NoError(t, err)
	assert.Nil(t, resolved)
}

func TestTokenExpiry(t *testing.T) {
	queries := setupTestDB(t)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries)
	ctx := context.Background()

	u, err := userStore.Create(ctx, db.InsertUserParams{
		Username: "expiryuser",
		Role:     "viewer",
	})
	require.NoError(t, err)

	// Create a token that expires in the past.
	past := time.Now().Add(-1 * time.Hour)
	plain, _, err := tokenStore.CreateToken(ctx, u.ID, "expired", &past)
	require.NoError(t, err)

	// Should not resolve (expired).
	resolved, err := tokenStore.ValidateAndResolve(ctx, plain, userStore)
	require.NoError(t, err)
	assert.Nil(t, resolved)
}

func TestTokenListAndDelete(t *testing.T) {
	queries := setupTestDB(t)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries)
	ctx := context.Background()

	u, err := userStore.Create(ctx, db.InsertUserParams{
		Username: "listuser",
		Role:     "admin",
	})
	require.NoError(t, err)

	_, tok1, err := tokenStore.CreateToken(ctx, u.ID, "tok-1", nil)
	require.NoError(t, err)
	_, _, err = tokenStore.CreateToken(ctx, u.ID, "tok-2", nil)
	require.NoError(t, err)

	// List should return 2.
	list, err := tokenStore.ListByUser(ctx, u.ID)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	// Delete one.
	err = tokenStore.Delete(ctx, tok1.ID)
	require.NoError(t, err)

	list, err = tokenStore.ListByUser(ctx, u.ID)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestExtractBearerToken(t *testing.T) {
	assert.Equal(t, "vgot_abc", ExtractBearerToken("Bearer vgot_abc"))
	assert.Equal(t, "vgot_abc", ExtractBearerToken("bearer vgot_abc"))
	assert.Equal(t, "", ExtractBearerToken(""))
	assert.Equal(t, "", ExtractBearerToken("Basic abc"))
	assert.Equal(t, "", ExtractBearerToken("Bear"))
}
