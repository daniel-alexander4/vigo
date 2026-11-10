// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
)

const (
	// apiTokenPrefix is prepended to all generated API tokens.
	apiTokenPrefix = "vgot_"
	// apiTokenBytes is the number of random bytes in a token (32 bytes = 64 hex chars).
	apiTokenBytes = 32
)

// hashAPIToken returns the SHA-256 hex digest of a token's plaintext.
// vgot_ tokens carry 256 bits of entropy from crypto/rand, so a fast
// hash with constant-time compare is the right primitive — bcrypt's
// slowness is wasted cost against high-entropy random input and turns
// validation into a DoS surface under sustained invalid traffic.
func hashAPIToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// GenerateAPIToken creates a new random API token with the vgot_ prefix.
// Returns the plaintext token (shown once to the user) and the SHA-256
// hex digest (stored in the database, indexed for O(1) lookup).
func GenerateAPIToken() (plaintext, hash string, err error) {
	b := make([]byte, apiTokenBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generating token: %w", err)
	}
	plaintext = apiTokenPrefix + hex.EncodeToString(b)
	return plaintext, hashAPIToken(plaintext), nil
}

// TokenStore provides API token CRUD operations.
type TokenStore struct {
	queries *db.Queries
	// localAdminToken is the root-only credential minted at boot
	// (localadmin.go). A bearer equal to it resolves to localAdminUser —
	// the server-host admin identity that replaced source-IP peer auth.
	localAdminToken string
	metrics         *metrics.Metrics
}

// NewTokenStore creates a TokenStore. localAdminToken is the boot-minted
// local-admin token (empty disables it — e.g. in tests). metrics carries
// the per-server counter set; tests pass metrics.NewTestMetrics().
func NewTokenStore(queries *db.Queries, localAdminToken string, m *metrics.Metrics) *TokenStore {
	return &TokenStore{queries: queries, localAdminToken: localAdminToken, metrics: m}
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

// ValidateAndResolve checks a bearer token and returns the owning user
// on success, nil on failure. Single indexed DB lookup, then constant-time
// compare on the row's stored hash (defense in depth; the SQL WHERE already
// determined the match). Updates last_used_at best-effort.
func (ts *TokenStore) ValidateAndResolve(ctx context.Context, plaintext string, userStore *UserStore) (*User, error) {
	if !strings.HasPrefix(plaintext, apiTokenPrefix) {
		return nil, nil
	}

	// The local-admin token resolves to the synthetic server-host admin.
	// It is never stored in api_tokens (the schema requires a real
	// user_id) — it is the 0600 root-only credential minted at boot.
	if ts.localAdminToken != "" &&
		subtle.ConstantTimeCompare([]byte(plaintext), []byte(ts.localAdminToken)) == 1 {
		return localAdminUser, nil
	}

	hash := hashAPIToken(plaintext)
	tok, err := ts.queries.LookupAPITokenByHash(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up token: %w", err)
	}

	// Defense in depth: the WHERE clause already matched, but compare in
	// constant time to keep the validation path uniform regardless of
	// future storage shape.
	if subtle.ConstantTimeCompare([]byte(tok.TokenHash), []byte(hash)) != 1 {
		return nil, nil
	}

	if tok.ExpiresAt.Valid && tok.ExpiresAt.Time.Before(time.Now()) {
		return nil, nil
	}

	if err := ts.queries.UpdateAPITokenLastUsed(ctx, tok.ID); err != nil {
		// Best-effort write: token validation already succeeded; an
		// audit-side last_used_at update failure shouldn't fail the
		// request. Surface via slog + counter so persistent DB unhealth
		// on the auth path doesn't hide behind "this token looks ancient"
		// in the admin UI / `vigocli tokens list`.
		slog.WarnContext(ctx, "api token last-used update failed",
			"token_id", tok.ID,
			"err", err,
		)
		ts.metrics.APITokenLastUsedUpdateErrors.Inc()
	}
	return userStore.GetByID(ctx, tok.UserID)
}

// ListByUser returns all tokens for a user (metadata only, no hashes).
func (ts *TokenStore) ListByUser(ctx context.Context, userID string) ([]db.ListAPITokensByUserRow, error) {
	return ts.queries.ListAPITokensByUser(ctx, userID)
}

// GetByID returns a token's metadata by ID. Returns sql.ErrNoRows if absent —
// callers (handleDelete) use it to authorize the owner before deleting.
func (ts *TokenStore) GetByID(ctx context.Context, id string) (db.ApiToken, error) {
	return ts.queries.GetAPITokenByID(ctx, id)
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
