// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAPIToken(t *testing.T) {
	plain, hash, err := GenerateAPIToken()
	require.NoError(t, err)

	// Token should have the prefix and the 64-char hex tail.
	assert.Equal(t, "vgot_", plain[:5])
	assert.Len(t, plain, 5+64)

	// Hash is the SHA-256 hex digest of the plaintext (64 chars).
	assert.Len(t, hash, 64)
	assert.Equal(t, hash, hashAPIToken(plain))

	// Different plaintext hashes differently.
	assert.NotEqual(t, hash, hashAPIToken("vgot_wrong"))
}

func TestTokenCreateAndValidate(t *testing.T) {
	queries := setupTestDB(t)
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries, testLocalAdminToken, metrics.NewTestMetrics())
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
	tokenStore := NewTokenStore(queries, testLocalAdminToken, metrics.NewTestMetrics())
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
	tokenStore := NewTokenStore(queries, testLocalAdminToken, metrics.NewTestMetrics())
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

// TestValidateAndResolve_LocalAdminToken pins the credential that replaced
// source-IP peer auth: a bearer equal to the boot-minted local-admin token
// resolves to the synthetic server-host admin, and nothing else does.
func TestValidateAndResolve_LocalAdminToken(t *testing.T) {
	queries := setupTestDB(t)
	userStore := NewUserStore(queries)
	ctx := context.Background()

	tokenStore := NewTokenStore(queries, testLocalAdminToken, metrics.NewTestMetrics())

	// The local-admin token resolves to the synthetic admin — no users row,
	// admin role, full permissions.
	resolved, err := tokenStore.ValidateAndResolve(ctx, testLocalAdminToken, userStore)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "localhost", resolved.Username)
	assert.Equal(t, "admin", resolved.Role)

	// A different vgot_ token is not the local-admin token.
	resolved, err = tokenStore.ValidateAndResolve(ctx, "vgot_"+strings.Repeat("b", 64), userStore)
	require.NoError(t, err)
	assert.Nil(t, resolved)

	// With no local-admin token configured, even the fixture is rejected.
	bare := NewTokenStore(queries, "", metrics.NewTestMetrics())
	resolved, err = bare.ValidateAndResolve(ctx, testLocalAdminToken, userStore)
	require.NoError(t, err)
	assert.Nil(t, resolved)
}

// BenchmarkValidateAndResolve pins the cost of the validation hot path.
// Pre-rewrite (bcrypt loop) this was ~n × ~100ms on miss; the SHA-256 +
// indexed-lookup shape should land in single-digit microseconds. The
// benchmark seeds a small token corpus and times miss-path lookups
// (the realistic adverse-traffic case the pending entry flagged).
func BenchmarkValidateAndResolve(b *testing.B) {
	queries := setupTestDB(&testing.T{})
	userStore := NewUserStore(queries)
	tokenStore := NewTokenStore(queries, "", metrics.NewTestMetrics())
	ctx := context.Background()

	u, err := userStore.Create(ctx, db.InsertUserParams{Username: "bench", Role: "viewer"})
	if err != nil {
		b.Fatalf("create user: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, _, err := tokenStore.CreateToken(ctx, u.ID, "tok", nil); err != nil {
			b.Fatalf("create token %d: %v", i, err)
		}
	}

	miss := "vgot_" + strings.Repeat("0", 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := tokenStore.ValidateAndResolve(ctx, miss, userStore)
		if err != nil {
			b.Fatalf("validate: %v", err)
		}
	}
}
