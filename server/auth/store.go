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
// In spanner mode the optional signer stamps origin_bolt + signature
// on every mutation (ADR-030) so the row is immediately replicatable;
// standalone mode passes nil signer and writes proceed without the
// stamping. Read paths are signer-agnostic.
type UserStore struct {
	queries *db.Queries
	signer  Signer // nil → standalone mode, no CRDT signing
}

// NewUserStore creates a UserStore. signer may be nil — in standalone
// mode the writes succeed without populating origin_bolt/signature.
func NewUserStore(queries *db.Queries) *UserStore {
	return &UserStore{queries: queries}
}

// SetSigner installs the CRDT row signer. Wired in server.go where the
// bolt identity is in scope (spanner mode); not called in standalone.
// A second SetSigner replaces the prior signer — supports test
// re-injection without a fresh store.
func (s *UserStore) SetSigner(signer Signer) {
	s.signer = signer
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

// Create inserts a new user. Stamps updated_at_ms + (if signer present)
// origin_bolt + signature internally per ADR-030.
func (s *UserStore) Create(ctx context.Context, params db.InsertUserParams) (*User, error) {
	// Reserve the synthetic-principal identifiers. The id is server-generated
	// (SQL DEFAULT), so username is the only attackable field here; the gossip
	// merge path guards id separately. Covers every local caller — REST,
	// CLI-via-REST, and OIDC just-in-time provisioning (untrusted IdP claim).
	if IsReservedPrincipal(params.Username) {
		return nil, ErrReservedUsername
	}
	params.UpdatedAtMs = time.Now().UnixMilli()
	// Sign over the canonical content. ID is unknown pre-insert (the
	// SQL DEFAULT generates it server-side), so the signature seals the
	// content with ID = "" — a peer reconstructs the same UserRow from
	// the row's wire shape EXCEPT for ID. The next mutation (which the
	// caller does to get the real ID from the returned row, then
	// Update) re-signs with the real ID. Until then the row is
	// inert-on-merge because peers reconstruct UserRow with the real ID
	// from the snapshot column, which won't match the signed content's
	// empty ID. To avoid that inert window: sign with the row's id only
	// AFTER the insert — but InsertUser returns the row, so a single
	// extra UPDATE-signature pass after the insert keeps the contract
	// simple. The pattern below does exactly that.
	u, err := s.queries.InsertUser(ctx, params)
	if err != nil {
		return nil, err
	}
	if s.signer != nil {
		row := UserRowFromDB(u)
		originBolt, sig, sErr := s.signer.Sign(&row)
		if sErr != nil {
			return nil, sErr
		}
		// Re-write origin_bolt + signature with the now-known ID. The
		// row's content (display name, role, etc.) is unchanged, so
		// updated_at_ms doesn't bump — keep the original.
		if uErr := s.queries.UpdateUser(ctx, db.UpdateUserParams{
			ID:           u.ID,
			DisplayName:  u.DisplayName,
			Email:        u.Email,
			Role:         u.Role,
			Permissions:  u.Permissions,
			SshPublicKey: u.SshPublicKey,
			OidcSubject:  u.OidcSubject,
			UpdatedAtMs:  u.UpdatedAtMs,
			OriginBolt:   originBolt,
			Signature:    sig,
		}); uErr != nil {
			return nil, uErr
		}
		u.OriginBolt = originBolt
		u.Signature = sig
	}
	return dbToUser(u), nil
}

// Update modifies an existing user. Stamps updated_at_ms + signs
// the new canonical content.
func (s *UserStore) Update(ctx context.Context, params db.UpdateUserParams) error {
	params.UpdatedAtMs = time.Now().UnixMilli()
	if s.signer != nil {
		row := UserRow{
			ID:           params.ID,
			DisplayName:  params.DisplayName,
			Email:        params.Email,
			Role:         params.Role,
			Permissions:  params.Permissions,
			SSHPublicKey: params.SshPublicKey,
			OIDCSubject:  params.OidcSubject,
			UpdatedAtMs:  params.UpdatedAtMs,
		}
		// Pull the existing row to fill username (immutable, not in
		// UpdateUserParams) so the signature covers the complete
		// canonical content peers will verify against.
		if existing, err := s.queries.GetUserByID(ctx, params.ID); err == nil {
			row.Username = existing.Username
			row.DeletedAt = nullInt64ToInt64(existing.DeletedAt)
		}
		originBolt, sig, err := s.signer.Sign(&row)
		if err != nil {
			return err
		}
		params.OriginBolt = originBolt
		params.Signature = sig
	}
	return s.queries.UpdateUser(ctx, params)
}

// Delete soft-removes a user by ID (ADR-030 tombstone). Stamps the
// tombstone's CRDT comparator + signs the tombstoned content, then
// runs the local api_tokens cascade — SQLite's ON DELETE CASCADE
// doesn't fire on the UPDATE that soft-delete uses, so an explicit
// DELETE keeps orphan tokens from authenticating for the now-deleted
// user.
func (s *UserStore) Delete(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	params := db.SoftDeleteUserParams{
		DeletedAt:   sql.NullInt64{Int64: now, Valid: true},
		UpdatedAtMs: now,
		ID:          id,
	}
	if s.signer != nil {
		// Reconstruct the canonical content as it will appear after
		// soft-delete so the signature covers the tombstoned shape.
		existing, err := s.queries.GetUserByID(ctx, id)
		if err != nil {
			return err
		}
		row := UserRowFromDB(existing)
		row.UpdatedAtMs = now
		row.DeletedAt = now
		originBolt, sig, sErr := s.signer.Sign(&row)
		if sErr != nil {
			return sErr
		}
		params.OriginBolt = originBolt
		params.Signature = sig
	}
	if err := s.queries.SoftDeleteUser(ctx, params); err != nil {
		return err
	}
	return s.queries.DeleteAPITokensForUser(ctx, id)
}

// List returns all live (non-tombstoned) users ordered by username.
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

// CountUsers returns the total number of live users.
func (s *UserStore) CountUsers(ctx context.Context) (int64, error) {
	return s.queries.CountUsers(ctx)
}

// CountAdmins returns the number of live admin users.
func (s *UserStore) CountAdmins(ctx context.Context) (int64, error) {
	return s.queries.CountAdminUsers(ctx)
}

// EnableTOTP marks a user as TOTP-enabled and stores hashed recovery
// codes. TOTP fields don't replicate (ADR-030 direction call A);
// updated_at_ms is NOT bumped — a local TOTP enrollment must not
// trigger empty CRDT churn on peers.
func (s *UserStore) EnableTOTP(ctx context.Context, id, recoveryJSON string) error {
	return s.queries.EnableTOTP(ctx, db.EnableTOTPParams{
		TotpRecovery: recoveryJSON,
		ID:           id,
	})
}

// DisableTOTP removes TOTP from a user. Bolt-local; no CRDT bump.
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

// UpdateRecoveryCodes updates the recovery codes JSON. Bolt-local; no
// CRDT bump (ADR-030 direction call A — TOTP fields don't replicate).
func (s *UserStore) UpdateRecoveryCodes(ctx context.Context, id, recoveryJSON string) error {
	return s.queries.UpdateTOTPRecovery(ctx, db.UpdateTOTPRecoveryParams{
		TotpRecovery: recoveryJSON,
		ID:           id,
	})
}

// ResignLegacyRows stamps origin_bolt + signature on every live user
// row that predates ADR-030 Slice 2-iii-c (signature IS NULL). Called
// once at server startup in spanner mode so legacy rows become
// eligible for snapshot replication. No-op without a signer (the
// standalone path; standalone-mode users stay locally-scoped).
//
// updated_at_ms is deliberately preserved as the migration default
// (0). Concurrent re-signs across bolts then tie on the LWW
// comparator and the lex-min-origin tiebreaker picks a deterministic
// winner — exactly the conflict-resolution rule ADR-030 promised.
// Bumping updated_at_ms to wall-clock would silently substitute
// "last bolt to boot wins" semantics; don't.
//
// Tombstoned legacy rows (deleted_at NOT NULL) are skipped — a
// local-only soft-delete must not propagate to peers that have a
// live signed version.
//
// Returns the count of rows actually re-signed.
func (s *UserStore) ResignLegacyRows(ctx context.Context) (int, error) {
	if s.signer == nil {
		return 0, nil
	}
	rows, err := s.queries.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	resigned := 0
	for _, u := range rows {
		if len(u.Signature) > 0 {
			continue // already signed
		}
		if u.DeletedAt.Valid {
			continue // tombstoned; don't propagate stale local delete
		}
		row := UserRowFromDB(u)
		originBolt, sig, sErr := s.signer.Sign(&row)
		if sErr != nil {
			return resigned, sErr
		}
		if rErr := s.queries.ResignUserLegacy(ctx, db.ResignUserLegacyParams{
			OriginBolt: originBolt,
			Signature:  sig,
			ID:         u.ID,
		}); rErr != nil {
			return resigned, rErr
		}
		resigned++
	}
	return resigned, nil
}

// CountUnsignedUsers returns the number of live user rows still
// missing a CRDT signature — the doctor check's input.
func (s *UserStore) CountUnsignedUsers(ctx context.Context) (int64, error) {
	return s.queries.CountUnsignedUsers(ctx)
}
