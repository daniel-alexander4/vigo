// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// UserStore provides user CRUD operations backed by sqlc queries.
type UserStore struct {
	queries *db.Queries
}

// NewUserStore creates a UserStore.
func NewUserStore(queries *db.Queries) *UserStore {
	return &UserStore{queries: queries}
}

// dbToUser converts a sqlc-generated db.User to an auth.User.
func dbToUser(u db.User) *User {
	var perms []string
	if u.Permissions != "" {
		if err := json.Unmarshal([]byte(u.Permissions), &perms); err != nil {
			perms = nil // fall through to role-based defaults
		}
	}
	// Fall back to role-based defaults if no explicit permissions stored.
	if len(perms) == 0 {
		perms = DefaultPermissions(u.Role)
	}
	return &User{
		ID:           u.ID,
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		Email:        u.Email,
		Role:         u.Role,
		Permissions:  perms,
		SSHPublicKey: u.SshPublicKey,
		TOTPEnabled:  u.TotpEnabled != 0,
	}
}

// GetByID looks up a user by ID. Returns nil, nil if not found.
func (s *UserStore) GetByID(ctx context.Context, id string) (*User, error) {
	u, err := s.queries.GetUserByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbToUser(u), nil
}

// GetByUsername looks up a user by username. Returns nil, nil if not found.
func (s *UserStore) GetByUsername(ctx context.Context, username string) (*User, error) {
	u, err := s.queries.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbToUser(u), nil
}

// GetByOIDCSubject looks up a user by OIDC subject. Returns nil, nil if not found.
func (s *UserStore) GetByOIDCSubject(ctx context.Context, subject string) (*User, error) {
	u, err := s.queries.GetUserByOIDCSubject(ctx, subject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbToUser(u), nil
}

// Create inserts a new user and returns it with the generated ID.
// Stamps updated_at_ms internally so external callers don't need to
// know about the CRDT comparator (ADR-030).
func (s *UserStore) Create(ctx context.Context, params db.InsertUserParams) (*User, error) {
	params.UpdatedAtMs = time.Now().UnixMilli()
	u, err := s.queries.InsertUser(ctx, params)
	if err != nil {
		return nil, err
	}
	return dbToUser(u), nil
}

// Update modifies an existing user. Stamps updated_at_ms internally.
func (s *UserStore) Update(ctx context.Context, params db.UpdateUserParams) error {
	params.UpdatedAtMs = time.Now().UnixMilli()
	return s.queries.UpdateUser(ctx, params)
}

// Delete soft-removes a user by ID (ADR-030 tombstone). Also runs the
// local api_tokens cascade — SQLite's ON DELETE CASCADE doesn't fire
// on the UPDATE that soft-delete uses, so an explicit DELETE keeps
// orphan tokens from authenticating for the now-deleted user.
func (s *UserStore) Delete(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	if err := s.queries.SoftDeleteUser(ctx, db.SoftDeleteUserParams{
		DeletedAt:   sql.NullInt64{Int64: now, Valid: true},
		UpdatedAtMs: now,
		ID:          id,
	}); err != nil {
		return err
	}
	return s.queries.DeleteAPITokensForUser(ctx, id)
}

// List returns all users ordered by username.
func (s *UserStore) List(ctx context.Context) ([]*User, error) {
	rows, err := s.queries.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]*User, len(rows))
	for i, r := range rows {
		users[i] = dbToUser(r)
	}
	return users, nil
}

// CountUsers returns the total number of users.
func (s *UserStore) CountUsers(ctx context.Context) (int64, error) {
	return s.queries.CountUsers(ctx)
}

// CountAdmins returns the number of admin users.
func (s *UserStore) CountAdmins(ctx context.Context) (int64, error) {
	return s.queries.CountAdminUsers(ctx)
}

// EnableTOTP marks a user as TOTP-enabled and stores hashed recovery codes.
func (s *UserStore) EnableTOTP(ctx context.Context, id, recoveryJSON string) error {
	return s.queries.EnableTOTP(ctx, db.EnableTOTPParams{
		TotpRecovery: recoveryJSON,
		UpdatedAtMs:  time.Now().UnixMilli(),
		ID:           id,
	})
}

// DisableTOTP removes TOTP from a user.
func (s *UserStore) DisableTOTP(ctx context.Context, id string) error {
	return s.queries.DisableTOTP(ctx, db.DisableTOTPParams{
		UpdatedAtMs: time.Now().UnixMilli(),
		ID:          id,
	})
}

// GetRecoveryCodes returns the raw recovery JSON for a user.
func (s *UserStore) GetRecoveryCodes(ctx context.Context, id string) (string, error) {
	u, err := s.queries.GetUserByID(ctx, id)
	if err != nil {
		return "", err
	}
	return u.TotpRecovery, nil
}

// UpdateRecoveryCodes updates the recovery codes JSON.
func (s *UserStore) UpdateRecoveryCodes(ctx context.Context, id, recoveryJSON string) error {
	return s.queries.UpdateTOTPRecovery(ctx, db.UpdateTOTPRecoveryParams{
		TotpRecovery: recoveryJSON,
		UpdatedAtMs:  time.Now().UnixMilli(),
		ID:           id,
	})
}
