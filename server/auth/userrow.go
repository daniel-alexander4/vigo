// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"database/sql"

	"git.alexander4.io/vigo/server/db"
)

// UserRow is the canonical content of a users row for cross-bolt
// replication (ADR-030). Eleven fields — every replicatable column.
// Excludes totp_enabled / totp_recovery per the Slice 2-iii-c
// direction call: TOTP enrollment is bolt-local, never replicated.
// Excludes created_at and updated_at (the DATETIME display column)
// since they aren't part of the LWW ordering — updated_at_ms is.
//
// The struct shape IS the canonical signing content: JSON marshalling
// of UserRow is what SignUserRow / VerifyUserRow sign and verify over.
// Field order matters for deterministic serialization; do not reorder.
type UserRow struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	Permissions  string `json:"permissions"`
	SSHPublicKey string `json:"ssh_public_key"`
	OIDCSubject  string `json:"oidc_subject"`
	UpdatedAtMs  int64  `json:"updated_at_ms"`
	OriginBolt   string `json:"origin_bolt"`
	DeletedAt    int64  `json:"deleted_at"` // 0 = live row
}

// UserRowFromDB projects a sqlc-generated db.User into the canonical
// replication content. Used by UserStore (when stamping signatures on
// write) and by the snapshot serve path (when projecting rows for
// gossip).
func UserRowFromDB(u db.User) UserRow {
	return UserRow{
		ID:           u.ID,
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		Email:        u.Email,
		Role:         u.Role,
		Permissions:  u.Permissions,
		SSHPublicKey: u.SshPublicKey,
		OIDCSubject:  u.OidcSubject,
		UpdatedAtMs:  u.UpdatedAtMs,
		OriginBolt:   u.OriginBolt,
		DeletedAt:    nullInt64ToInt64(u.DeletedAt),
	}
}

func nullInt64ToInt64(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// Signer signs a UserRow with the local bolt's identity and returns
// the origin pubkey-hex + ed25519 signature over the row's canonical
// content. The spanner package provides the production implementation
// (BoltSigner wrapping bolt.Identity); standalone-mode UserStore takes
// a nil Signer and leaves origin_bolt / signature empty.
type Signer interface {
	Sign(row *UserRow) (originPubkeyHex string, signature []byte, err error)
}
