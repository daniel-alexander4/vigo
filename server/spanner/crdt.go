// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package spanner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"log/slog"
	"sort"
	"strconv"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// Spanner Slice 2-i-a — the CRDT merge layer (ADR-026 + the
// spanner-redesign plan). Slice 2A.2 shipped the membership ceremony
// (init/invite/join over hand-written REST); 2-i-a makes the roster
// *replicate* peer-to-peer: every non-standalone bolt periodically
// pulls every roster peer's full snapshot over REST and CRDT-merges
// it, so all bolts converge on one admissions roster.
//
// The two CRDTs:
//
//   - spanner_bolts — an LWW-Map keyed by pubkey. The merge is
//     MergeBoltLWW (an ON CONFLICT(pubkey) DO UPDATE upsert whose
//     WHERE clause encodes the LWW comparator and absolute
//     revoke-wins). This file does NOT reimplement that comparator —
//     it lives in the SQL.
//
//   - spanner_consumed_tokens — a G-Set keyed by token_hash. The
//     merge is MergeConsumedToken (an ON CONFLICT(token_hash) DO
//     NOTHING insert): re-applying a token is a no-op, so a snapshot
//     union is idempotent and order-independent.
//
// Merge commutativity holds because both per-row merges are
// commutative: the LWW comparator is a total order — newer admitted_at
// wins; at equal admitted_at revoked beats live; and between two
// revocations the smallest (revoked_at, revoked_by) tuple wins — so the
// winner is independent of apply order, and the G-Set union is set
// union. Applying snapshot A then B yields the same DB state as B
// then A. (Without the revoke-vs-revoke tiebreak, concurrent revokes
// of one bolt wedged both merge directions and the Tier-1 digests —
// which hash the exact revoked_at — diverged permanently.)

// RosterRow is the JSON wire projection of a spanner_bolts row — the
// canonical shape that crosses /snapshot, /admit, /join, and /roster.
// Nullable columns are flattened (consumed_token_hash + revoked_* may
// be ""); signature is base64 std (Go's []byte JSON encoding). A peer
// reconstructs the canonical AdmissionRow from it to verify the
// per-row signature end-to-end.
type RosterRow struct {
	Pubkey            string `json:"pubkey"`
	Hostname          string `json:"hostname"`
	AdmittedAt        int64  `json:"admitted_at"`
	AdmittedBy        string `json:"admitted_by"`
	ConsumedTokenHash string `json:"consumed_token_hash,omitempty"`
	RevokedAt         int64  `json:"revoked_at,omitempty"`
	RevokedBy         string `json:"revoked_by,omitempty"`
	PatternClaims     string `json:"pattern_claims"`
	Signature         []byte `json:"signature"`
}

// ConsumedToken is the JSON wire projection of a
// spanner_consumed_tokens row — the G-Set element. It travels
// alongside the roster so a merging peer can satisfy the
// spanner_bolts.consumed_token_hash foreign key (token row merged
// first).
type ConsumedToken struct {
	TokenHash      string `json:"token_hash"`
	ConsumedAt     int64  `json:"consumed_at"`
	ConsumedBy     string `json:"consumed_by"`
	AdmittedPubkey string `json:"admitted_pubkey"`
	ExpiresAt      int64  `json:"expires_at"`
}

// Snapshot is one bolt's full CRDT state: the admissions roster +
// consumed-token G-Set + (ADR-030 Slice 2-iii-c) the webusers
// LWW-Map. Payload of GET /api/v1/spanner/snapshot and the input to
// MergeSnapshot.
type Snapshot struct {
	Roster         []RosterRow       `json:"roster"`
	ConsumedTokens []ConsumedToken   `json:"consumed_tokens"`
	Users          []UserSnapshotRow `json:"users,omitempty"`
}

// UserSnapshotRow is the wire shape of a users-table row for ADR-030
// gossip. Embeds the canonical signed content (auth.UserRow) +
// signature. Slice 2-iii-d's MergeUsers reconstructs UserRow from the
// embedded fields and verifies Signature against the origin bolt's
// pubkey looked up in the local roster.
type UserSnapshotRow struct {
	auth.UserRow
	Signature []byte `json:"signature"`
}

// UserSnapshotRowFromDB projects a sqlc-generated db.User into the
// wire shape. Skips the totp_* fields (not in UserRow per the
// Slice 2-iii-c direction call) and carries the signature verbatim.
func UserSnapshotRowFromDB(u db.User) UserSnapshotRow {
	return UserSnapshotRow{
		UserRow:   auth.UserRowFromDB(u),
		Signature: u.Signature,
	}
}

// AdmissionRowOf rebuilds the canonical AdmissionRow from a RosterRow
// so its signature can be verified against the admitting bolt's
// pubkey. revoked_* is deliberately excluded — the admission
// signature covers identity + grant fields only (see AdmissionRow).
func (r RosterRow) AdmissionRowOf() AdmissionRow {
	return AdmissionRow{
		Pubkey:            r.Pubkey,
		Hostname:          r.Hostname,
		AdmittedAt:        r.AdmittedAt,
		AdmittedBy:        r.AdmittedBy,
		ConsumedTokenHash: r.ConsumedTokenHash,
		PatternClaims:     r.PatternClaims,
	}
}

// RosterRowFromBolt projects a stored db.SpannerBolt into the wire
// shape.
func RosterRowFromBolt(b db.SpannerBolt) RosterRow {
	rr := RosterRow{
		Pubkey:        b.Pubkey,
		Hostname:      b.Hostname,
		AdmittedAt:    b.AdmittedAt,
		AdmittedBy:    b.AdmittedBy,
		PatternClaims: b.PatternClaims,
		Signature:     b.Signature,
	}
	if b.ConsumedTokenHash.Valid {
		rr.ConsumedTokenHash = b.ConsumedTokenHash.String
	}
	if b.RevokedAt.Valid {
		rr.RevokedAt = b.RevokedAt.Int64
	}
	if b.RevokedBy.Valid {
		rr.RevokedBy = b.RevokedBy.String
	}
	return rr
}

// mergeBoltLWWParams builds MergeBoltLWWParams from a wire RosterRow.
func (r RosterRow) mergeBoltLWWParams() db.MergeBoltLWWParams {
	p := db.MergeBoltLWWParams{
		Pubkey:        r.Pubkey,
		Hostname:      r.Hostname,
		AdmittedAt:    r.AdmittedAt,
		AdmittedBy:    r.AdmittedBy,
		PatternClaims: r.PatternClaims,
		Signature:     r.Signature,
	}
	if r.ConsumedTokenHash != "" {
		p.ConsumedTokenHash = sql.NullString{String: r.ConsumedTokenHash, Valid: true}
	}
	if r.RevokedAt != 0 {
		p.RevokedAt = sql.NullInt64{Int64: r.RevokedAt, Valid: true}
	}
	if r.RevokedBy != "" {
		p.RevokedBy = sql.NullString{String: r.RevokedBy, Valid: true}
	}
	return p
}

// FounderPubkey resolves the founder's raw Ed25519 pubkey from a local
// roster. The founder is the self-admit row: pubkey == admitted_by AND
// consumed_token_hash is NULL (it consumed no join token). Returns an
// error if no founder row exists locally (the caller logs it and skips
// the merge cycle) or if exactly one cannot be identified.
func FounderPubkey(roster []db.SpannerBolt) (ed25519.PublicKey, error) {
	var founderHex string
	for _, b := range roster {
		if b.Pubkey == b.AdmittedBy && !b.ConsumedTokenHash.Valid {
			if founderHex != "" && founderHex != b.Pubkey {
				return nil, fmt.Errorf("spanner crdt: multiple founder self-admit rows in roster (%s, %s)", founderHex, b.Pubkey)
			}
			founderHex = b.Pubkey
		}
	}
	if founderHex == "" {
		return nil, fmt.Errorf("spanner crdt: no founder self-admit row in local roster")
	}
	raw, err := hex.DecodeString(founderHex)
	if err != nil {
		return nil, fmt.Errorf("spanner crdt: founder pubkey %q is not hex: %w", founderHex, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("spanner crdt: founder pubkey is %d bytes, expected %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// Tier1Digest returns a hex-encoded sha256 over the spanner's full
// replicated state — the Tier-1 change-detector that fires Tier-2
// pulls on mismatch (Slice 2-i-b for admissions; Slice 2-iii-e for
// webusers). Hash inputs in two sections:
//
//  1. Roster — sorted by pubkey, each bolt contributes
//     (pubkey, admitted_at, revoked_at, signature). Same fields as
//     the original RosterDigest (Slice 2-i-b).
//
//  2. Users — sorted by id, each signed user row contributes
//     (id, updated_at_ms, origin_bolt, deleted_at, signature).
//     Unsigned rows (signature NULL — pre-replication legacy data
//     awaiting Slice 2-iii-f's first-startup re-sign) are skipped
//     so they don't force Tier-2 pulls that would no-op (the
//     snapshot endpoint excludes them via the same filter).
//
// A peer hearing a digest different from its own fires the existing
// Tier-2 pullAndMerge immediately instead of waiting for the 5m
// ticker. Consumed-tokens are NOT in the digest — they're a G-Set
// FK-bound to admissions and cannot drift independently.
func Tier1Digest(roster []db.SpannerBolt, users []db.User) string {
	sortedBolts := make([]db.SpannerBolt, len(roster))
	copy(sortedBolts, roster)
	sort.Slice(sortedBolts, func(i, j int) bool { return sortedBolts[i].Pubkey < sortedBolts[j].Pubkey })

	h := sha256.New()
	for _, b := range sortedBolts {
		// Length-prefix every field so concatenation is unambiguous —
		// no field boundary can be forged by shifting bytes between
		// adjacent fields.
		writeField(h, []byte(b.Pubkey))
		writeField(h, []byte(strconv.FormatInt(b.AdmittedAt, 10)))
		revokedAt := int64(0)
		if b.RevokedAt.Valid {
			revokedAt = b.RevokedAt.Int64
		}
		writeField(h, []byte(strconv.FormatInt(revokedAt, 10)))
		writeField(h, b.Signature)
	}

	// Section 2: signed users only, sorted by id. Pre-allocate to
	// the upper bound — pre-replication rows get filtered out below.
	sortedUsers := make([]db.User, 0, len(users))
	for _, u := range users {
		if len(u.Signature) == 0 {
			continue
		}
		sortedUsers = append(sortedUsers, u)
	}
	sort.Slice(sortedUsers, func(i, j int) bool { return sortedUsers[i].ID < sortedUsers[j].ID })

	for _, u := range sortedUsers {
		writeField(h, []byte(u.ID))
		writeField(h, []byte(strconv.FormatInt(u.UpdatedAtMs, 10)))
		writeField(h, []byte(u.OriginBolt))
		deletedAt := int64(0)
		if u.DeletedAt.Valid {
			deletedAt = u.DeletedAt.Int64
		}
		writeField(h, []byte(strconv.FormatInt(deletedAt, 10)))
		writeField(h, u.Signature)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeField hashes a length-prefixed field into h so the digest is
// unambiguous across field boundaries — no byte can be shifted between
// adjacent fields without changing the hash.
func writeField(h hash.Hash, b []byte) {
	var lenbuf [8]byte
	binary.LittleEndian.PutUint64(lenbuf[:], uint64(len(b)))
	_, _ = h.Write(lenbuf[:])
	_, _ = h.Write(b)
}

// VerifyRoster is the trust filter every wire roster passes before it
// touches the DB (gossip MergeSnapshot and the join ceremony alike). It
// returns:
//
//   - the rows whose admission is founder-issued and founder-signed
//     (admitted_by == founder AND the signature verifies), each with its
//     revoked_* fields STRIPPED. Revocation is outside the signed
//     admission row and no signed revocation record exists yet (the
//     bolt-revocation feature is deferred; nothing in production creates
//     a revocation), so an unsigned revoked_at/revoked_by from the wire is
//     an unauthenticated claim — accepting it let any MITM or admitted
//     bolt permanently revoke every bolt, founder included, under absolute
//     revoke-wins. A locally-recorded revocation still survives a live
//     wire row (MergeBoltLWW's revoke-wins guard).
//   - the consumed-token G-Set entries bound to one of those rows (some
//     verified row's founder-signed consumed_token_hash names it). An
//     unbound entry is an unauthenticated claim that a token was spent —
//     accepting it let a peer pre-burn outstanding invite tokens.
func VerifyRoster(roster []RosterRow, tokens []ConsumedToken, founderPubkey ed25519.PublicKey) ([]RosterRow, []ConsumedToken) {
	founderHex := hex.EncodeToString(founderPubkey)
	verified := make([]RosterRow, 0, len(roster))
	bound := make(map[string]bool)
	for _, rr := range roster {
		if rr.AdmittedBy != founderHex {
			slog.Warn("spanner crdt: skipping roster row not admitted by the founder",
				"pubkey", rr.Pubkey, "admitted_by", rr.AdmittedBy)
			continue
		}
		arow := rr.AdmissionRowOf()
		if vErr := VerifyAdmissionRow(founderPubkey, &arow, rr.Signature); vErr != nil {
			slog.Warn("spanner crdt: skipping roster row with invalid signature",
				"pubkey", rr.Pubkey, "error", vErr)
			continue
		}
		if rr.RevokedAt != 0 || rr.RevokedBy != "" {
			slog.Warn("spanner crdt: refusing unsigned revocation from the wire",
				"pubkey", rr.Pubkey, "revoked_at", rr.RevokedAt, "revoked_by", rr.RevokedBy)
			rr.RevokedAt, rr.RevokedBy = 0, ""
		}
		if rr.ConsumedTokenHash != "" {
			bound[rr.ConsumedTokenHash] = true
		}
		verified = append(verified, rr)
	}
	keptTokens := make([]ConsumedToken, 0, len(tokens))
	for _, tok := range tokens {
		if !bound[tok.TokenHash] {
			slog.Warn("spanner crdt: refusing consumed token not bound to a signed admission",
				"token_hash", tok.TokenHash)
			continue
		}
		keptTokens = append(keptTokens, tok)
	}
	return verified, keptTokens
}

// MergeSnapshot CRDT-merges a peer's Snapshot into the local DB and
// returns how many spanner_bolts rows the LWW merge actually mutated.
//
// The snapshot passes VerifyRoster first: only founder-signed admission
// rows (revocations stripped) and the consumed tokens they bind reach
// the DB.
//
// The whole merge runs in one sql.Tx (mirroring persistRoster): the
// consumed-token G-Set is unioned first because it is the FK target
// of spanner_bolts.consumed_token_hash, then every verified bolt row
// is run through MergeBoltLWW.
func MergeSnapshot(ctx context.Context, q *db.Queries, sqlDB *sql.DB, snap Snapshot, founderPubkey ed25519.PublicKey, sessions *auth.SessionManager, audit AuditRecorder) (merged int, err error) {
	if len(founderPubkey) != ed25519.PublicKeySize {
		return 0, fmt.Errorf("spanner crdt: founder pubkey is %d bytes, expected %d", len(founderPubkey), ed25519.PublicKeySize)
	}

	// Verify up front. A row or token that fails is dropped here and
	// never enters the transaction.
	verified, tokens := VerifyRoster(snap.Roster, snap.ConsumedTokens, founderPubkey)

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("spanner crdt: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after Commit is a no-op

	qtx := q.WithTx(tx)

	// Token G-Set union first — FK targets of spanner_bolts. The
	// G-Set merge is idempotent (ON CONFLICT DO NOTHING), so a
	// re-applied snapshot is a no-op.
	for _, tok := range tokens {
		if _, mErr := qtx.MergeConsumedToken(ctx, db.MergeConsumedTokenParams{
			TokenHash:      tok.TokenHash,
			ConsumedAt:     tok.ConsumedAt,
			ConsumedBy:     tok.ConsumedBy,
			AdmittedPubkey: tok.AdmittedPubkey,
			ExpiresAt:      tok.ExpiresAt,
		}); mErr != nil {
			return 0, fmt.Errorf("spanner crdt: merge consumed token %s: %w", tok.TokenHash, mErr)
		}
	}

	// Bolt LWW-Map merge. RowsAffected is non-zero only when the LWW
	// comparator decided this snapshot row wins — that count is the
	// number of rows the merge actually mutated.
	for _, rr := range verified {
		affected, mErr := qtx.MergeBoltLWW(ctx, rr.mergeBoltLWWParams())
		if mErr != nil {
			return 0, fmt.Errorf("spanner crdt: merge bolt %s: %w", rr.Pubkey, mErr)
		}
		merged += int(affected)
	}

	// ADR-030 Slice 2-iii-d: webusers LWW-Map merge rides the same
	// transaction so the api_tokens cascade (on fresh tombstone) is
	// atomic with the user-row soft-delete. Side-effect dispatch
	// (DestroyUserSessions + audit emission) deliberately runs
	// AFTER commit — see dispatchUserEffects.
	// Known bolts come from the LOCAL roster as it stands after the bolt
	// merge, never from the wire rows — a locally-revoked bolt stays
	// revoked for user-row authorization whatever the peer claims.
	localRoster, lErr := qtx.ListBolts(ctx)
	if lErr != nil {
		return 0, fmt.Errorf("spanner crdt: reading merged roster: %w", lErr)
	}
	userEffects, mErr := MergeUsers(ctx, qtx, snap.Users, buildKnownBolts(localRoster))
	if mErr != nil {
		return 0, mErr
	}
	merged += len(userEffects)

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("spanner crdt: commit: %w", err)
	}

	dispatchUserEffects(ctx, sessions, audit, userEffects)
	return merged, nil
}
