// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// ADR-030 Slice 2-iii-d -- merge half of the webusers CRDT.
//
// MergeUsers is the per-CRDT equivalent of the bolts merge in
// MergeSnapshot: verify-then-merge-in-tx, with two additions specific
// to the user lifecycle:
//
//   1. Pre-merge GetUserByID inside the transaction reads the local
//      row (if any) so the caller can tell what the merge actually
//      changed -- role flip, fresh tombstone, or benign metadata.
//
//   2. Fresh-tombstone path runs DeleteAPITokensForUser in the same
//      transaction. SQLite's ON DELETE CASCADE on api_tokens.user_id
//      doesn't fire on the soft-delete UPDATE, so atomicity must be
//      enforced here -- a soft-deleted user can never authenticate
//      via leftover tokens, even momentarily.
//
// Side-effect dispatch (DestroyUserSessions + audit emission) happens
// in MergeSnapshot AFTER tx commit. Doing it in-tx would risk
// destroying sessions for a row that then failed to commit; doing it
// post-commit means an SCS hiccup can't roll back the merge.

// UserMergeEffect names a side effect a merge accepted -- consumed
// by MergeSnapshot's post-commit loop to fire DestroyUserSessions +
// audit events. RoleChanged and Tombstoned can both be true (a
// peer demoting + deleting at once), and the post-commit handler
// dedupes by firing one DestroyUserSessions per effect regardless.
type UserMergeEffect struct {
	UserID       string
	OriginBolt   string // pubkey-hex of the writing bolt; the audit actor
	RoleChanged  bool
	Tombstoned   bool
}

// knownBolt is the subset of spanner_bolts MergeUsers needs to
// verify per-user signatures: pubkey + revoked-or-not. Built once
// from the roster at the start of MergeSnapshot so each user-row
// verify is a constant-time map lookup, not an O(N_bolts) scan.
type knownBolt struct {
	pubkey  ed25519.PublicKey
	revoked bool
}

// MergeUsers verifies and applies each UserSnapshotRow against the
// local users table, atomically running the api_tokens cascade when
// a fresh tombstone is accepted. Returns the list of effects the
// post-commit dispatcher acts on (one per accepted row that changed
// role or flipped to deleted).
//
// Runs entirely inside qtx -- the caller's transaction. A row that
// fails any check (origin not in roster, origin revoked, signature
// invalid) is dropped and logged; one bad row never aborts the
// whole batch.
func MergeUsers(ctx context.Context, qtx *db.Queries, users []UserSnapshotRow, knownBolts map[string]knownBolt) ([]UserMergeEffect, error) {
	effects := make([]UserMergeEffect, 0, len(users))
	for _, ur := range users {
		// Verify origin bolt is a known live roster member. A row
		// whose origin isn't in the roster means either (a) the
		// origin bolt was admitted by us but we haven't merged its
		// admission yet (out-of-order pull -- skip, next cycle will
		// see it), or (b) the origin is forged. Either way, skip.
		kb, ok := knownBolts[ur.OriginBolt]
		if !ok {
			slog.Warn("spanner crdt: skipping user row from unknown origin bolt",
				"user_id", ur.ID, "origin_bolt", ur.OriginBolt)
			continue
		}
		if kb.revoked {
			slog.Warn("spanner crdt: skipping user row from revoked origin bolt",
				"user_id", ur.ID, "origin_bolt", ur.OriginBolt)
			continue
		}
		// Verify the signature against the origin's pubkey.
		content := ur.UserRow
		if vErr := VerifyUserRow(kb.pubkey, &content, ur.Signature); vErr != nil {
			slog.Warn("spanner crdt: skipping user row with invalid signature",
				"user_id", ur.ID, "origin_bolt", ur.OriginBolt, "error", vErr)
			continue
		}
		// Reject rows claiming a reserved synthetic-principal identifier. A
		// guarded peer can't originate one (UserStore.Create blocks it), so
		// this only fires on a forged or stale-version origin — but a row with
		// id == LocalAdminUserID would defeat the token-owner orphan guard on
		// every receiving bolt, so drop it here too.
		if vigoauth.IsReservedPrincipal(content.ID) || vigoauth.IsReservedPrincipal(content.Username) {
			slog.Warn("spanner crdt: skipping user row with reserved principal id/username",
				"user_id", ur.ID, "username", ur.Username, "origin_bolt", ur.OriginBolt)
			continue
		}

		// Pre-merge read for change detection. NotFound is fine -- a
		// brand-new user has no prior row.
		pre, preErr := qtx.GetUserByID(ctx, ur.ID)
		preExists := preErr == nil

		// Apply the LWW merge. RowsAffected reports whether the
		// comparator decided this incoming row wins.
		params := db.MergeUserLWWParams{
			ID:           ur.ID,
			Username:     ur.Username,
			DisplayName:  ur.DisplayName,
			Email:        ur.Email,
			Role:         ur.Role,
			Permissions:  ur.Permissions,
			SshPublicKey: ur.SSHPublicKey,
			OidcSubject:  ur.OIDCSubject,
			UpdatedAtMs:  ur.UpdatedAtMs,
			OriginBolt:   ur.OriginBolt,
			Signature:    ur.Signature,
		}
		if ur.DeletedAt != 0 {
			params.DeletedAt = sql.NullInt64{Int64: ur.DeletedAt, Valid: true}
		}
		affected, mErr := qtx.MergeUserLWW(ctx, params)
		if mErr != nil {
			return nil, fmt.Errorf("spanner crdt: merge user %s: %w", ur.ID, mErr)
		}
		if affected == 0 {
			// LWW comparator said local wins -- no change, no effect.
			continue
		}

		// The merge took effect. Compute change-detect against the
		// pre-merge row.
		effect := UserMergeEffect{
			UserID:     ur.ID,
			OriginBolt: ur.OriginBolt,
		}
		preDeleted := preExists && pre.DeletedAt.Valid
		if preExists && pre.Role != ur.Role {
			effect.RoleChanged = true
		}
		if ur.DeletedAt != 0 && !preDeleted {
			// Fresh tombstone -- run the api_tokens cascade in-tx so
			// the soft-deleted user can't authenticate via leftover
			// tokens. The api_tokens table doesn't replicate; each
			// peer enforces its own cascade.
			if cErr := qtx.DeleteAPITokensForUser(ctx, ur.ID); cErr != nil {
				return nil, fmt.Errorf("spanner crdt: cascade api_tokens for %s: %w", ur.ID, cErr)
			}
			effect.Tombstoned = true
		}

		if effect.RoleChanged || effect.Tombstoned {
			effects = append(effects, effect)
		}
	}
	return effects, nil
}

// buildKnownBolts projects the verified roster into the per-pubkey
// lookup MergeUsers consumes. Skips rows whose pubkey-hex doesn't
// decode to a valid 32-byte Ed25519 key -- those can never be the
// signer of a user row that verifies anyway.
func buildKnownBolts(roster []RosterRow) map[string]knownBolt {
	out := make(map[string]knownBolt, len(roster))
	for _, rr := range roster {
		raw, err := hex.DecodeString(rr.Pubkey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		out[rr.Pubkey] = knownBolt{
			pubkey:  ed25519.PublicKey(raw),
			revoked: rr.RevokedAt != 0,
		}
	}
	return out
}

// AuditRecorder is the minimal interface MergeSnapshot uses for
// audit emission. nil-safe via recordReplicationAudit. Mirrors the
// api package's auditRecorder pattern so spanner stays decoupled
// from server/audit's concrete Writer.
type AuditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

func recordReplicationAudit(ctx context.Context, w AuditRecorder, eventType, actor, summary string) {
	if w == nil {
		return
	}
	if err := w.Record(ctx, eventType, actor, "spanner_gossip", summary, ""); err != nil {
		slog.Error("spanner crdt: audit record failed", "type", eventType, "error", err)
	}
}

// dispatchUserEffects fires session-destroy + audit-emit for each
// effect post-commit. Per-effect errors are logged but never abort
// the loop -- a bad SCS read for one user shouldn't strand the
// rest. session-destroy is a no-op when sm is nil (standalone
// mode + tests).
func dispatchUserEffects(ctx context.Context, sm *vigoauth.SessionManager, audit AuditRecorder, effects []UserMergeEffect) {
	for _, e := range effects {
		if e.Tombstoned {
			recordReplicationAudit(ctx, audit, "webusers.tombstoned_via_merge", e.OriginBolt,
				fmt.Sprintf("user %s tombstoned by bolt %s", e.UserID, e.OriginBolt))
		}
		if e.RoleChanged {
			recordReplicationAudit(ctx, audit, "webusers.role_changed_via_merge", e.OriginBolt,
				fmt.Sprintf("user %s role changed via merge from bolt %s", e.UserID, e.OriginBolt))
		}
		if sm != nil {
			if n, err := sm.DestroyUserSessions(ctx, e.UserID); err != nil {
				slog.Warn("spanner crdt: destroying sessions post-merge failed",
					"user_id", e.UserID, "destroyed", n, "error", err)
			}
		}
	}
}
