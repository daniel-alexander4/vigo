// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hobnob/hobnob/server/db"
	"golang.org/x/crypto/bcrypt"
)

const (
	// apiTokenPrefix is prepended to all generated API tokens.
	apiTokenPrefix = "hbt_"
	// apiTokenBytes is the number of random bytes in a token (32 bytes = 64 hex chars).
	apiTokenBytes = 32
)

// GenerateAPIToken creates a new random API token with the hbt_ prefix.
// Returns the plaintext token (shown once to the user) and the bcrypt hash
// (stored in the database).
func GenerateAPIToken() (plaintext, hash string, err error) {
	b := make([]byte, apiTokenBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generating token: %w", err)
	}
	plaintext = apiTokenPrefix + hex.EncodeToString(b)
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("hashing token: %w", err)
	}
	return plaintext, string(h), nil
}

// ValidateAPIToken checks a plaintext token against a bcrypt hash.
func ValidateAPIToken(plaintext, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// TokenStore provides API token CRUD operations.
type TokenStore struct {
	queries *db.Queries
}

// NewTokenStore creates a TokenStore.
func NewTokenStore(queries *db.Queries) *TokenStore {
	return &TokenStore{queries: queries}
}

// CreateToken generates a new API token for the given user.
// Returns the plaintext token (displayed once) and the token metadata.
func (ts *TokenStore) CreateToken(ctx context.Context, userID, name string, expiresAt *time.Time) (plaintext string, token db.ApiToken, err error) {
	plain, hash, err := GenerateAPIToken()
	if err != nil {
		return "", db.ApiToken{}, err
	}

	var expiry sql.NullTime
	if expiresAt != nil {
		expiry = sql.NullTime{Time: *expiresAt, Valid: true}
	}

	token, err = ts.queries.InsertAPIToken(ctx, db.InsertAPITokenParams{
		UserID:    userID,
		Name:      name,
		TokenHash: hash,
		ExpiresAt: expiry,
	})
	if err != nil {
		return "", db.ApiToken{}, fmt.Errorf("storing token: %w", err)
	}

	return plain, token, nil
}

// ValidateAndResolve checks a bearer token against all stored tokens.
// Returns the owning user on success, nil on failure. Updates last_used_at.
func (ts *TokenStore) ValidateAndResolve(ctx context.Context, plaintext string, userStore *UserStore) (*User, error) {
	if !strings.HasPrefix(plaintext, apiTokenPrefix) {
		return nil, nil
	}

	tokens, err := ts.queries.ListAllAPITokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}

	for _, tok := range tokens {
		// Check expiry first (cheap).
		if tok.ExpiresAt.Valid && tok.ExpiresAt.Time.Before(time.Now()) {
			continue
		}

		if !ValidateAPIToken(plaintext, tok.TokenHash) {
			continue
		}

		// Match found — update last_used_at (best effort).
		_ = ts.queries.UpdateAPITokenLastUsed(ctx, tok.ID)

		return userStore.GetByID(ctx, tok.UserID)
	}

	return nil, nil
}

// ListByUser returns all tokens for a user (metadata only, no hashes).
func (ts *TokenStore) ListByUser(ctx context.Context, userID string) ([]db.ListAPITokensByUserRow, error) {
	return ts.queries.ListAPITokensByUser(ctx, userID)
}

// Delete removes a token by ID.
func (ts *TokenStore) Delete(ctx context.Context, id string) error {
	return ts.queries.DeleteAPIToken(ctx, id)
}

// ExtractBearerToken extracts the token from an Authorization header.
// Returns empty string if the header is missing or not a Bearer token.
func ExtractBearerToken(authHeader string) string {
	const prefix = "Bearer "
	if len(authHeader) < len(prefix) {
		return ""
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(authHeader[:len(prefix)])), []byte("bearer ")) != 1 {
		return ""
	}
	return authHeader[len(prefix):]
}
