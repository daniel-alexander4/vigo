// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hobnob/hobnob/server/db"
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
		_ = json.Unmarshal([]byte(u.Permissions), &perms)
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
func (s *UserStore) Create(ctx context.Context, params db.InsertUserParams) (*User, error) {
	u, err := s.queries.InsertUser(ctx, params)
	if err != nil {
		return nil, err
	}
	return dbToUser(u), nil
}

// Update modifies an existing user.
func (s *UserStore) Update(ctx context.Context, params db.UpdateUserParams) error {
	return s.queries.UpdateUser(ctx, params)
}

// Delete removes a user by ID.
func (s *UserStore) Delete(ctx context.Context, id string) error {
	return s.queries.DeleteUser(ctx, id)
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
	return s.queries.EnableTOTP(ctx, db.EnableTOTPParams{TotpRecovery: recoveryJSON, ID: id})
}

// DisableTOTP removes TOTP from a user.
func (s *UserStore) DisableTOTP(ctx context.Context, id string) error {
	return s.queries.DisableTOTP(ctx, id)
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
	return s.queries.UpdateTOTPRecovery(ctx, db.UpdateTOTPRecoveryParams{TotpRecovery: recoveryJSON, ID: id})
}
