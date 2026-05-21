// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
// commutative: the LWW comparator is a total order on (admitted_at,
// revoke-state) so the winner is independent of apply order, and the
// G-Set union is set union. Applying snapshot A then B yields the
// same DB state as B then A.

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

// Snapshot is one bolt's full roster + consumed-token G-Set, the
// payload of GET /api/v1/spanner/snapshot and the input to
// MergeSnapshot.
type Snapshot struct {
	Roster         []RosterRow     `json:"roster"`
	ConsumedTokens []ConsumedToken `json:"consumed_tokens"`
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

// RosterDigest returns a hex-encoded sha256 over the roster — the
// Tier-1 change-detector (Slice 2-i-b). Rows are sorted by pubkey so
// the digest is independent of DB row order, and each row contributes
// (pubkey, admitted_at, revoked_at, signature): the fields a CRDT
// merge can mutate. An unchanged roster always hashes to the same
// value; any row added, re-admitted (admitted_at moved), revoked, or
// re-signed changes it. A peer hearing a digest different from its
// own fires the existing Tier-2 pullAndMerge immediately instead of
// waiting for the 5m timer.
func RosterDigest(roster []db.SpannerBolt) string {
	sorted := make([]db.SpannerBolt, len(roster))
	copy(sorted, roster)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Pubkey < sorted[j].Pubkey })

	h := sha256.New()
	for _, b := range sorted {
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

// MergeSnapshot CRDT-merges a peer's Snapshot into the local DB and
// returns how many spanner_bolts rows the LWW merge actually mutated.
//
// Every bolt row is verified end-to-end before it is merged: in
// Slice 2-i-a every admission is founder-issued, so a row is accepted
// only when its admitted_by equals the founder's pubkey-hex AND its
// signature verifies against the founder's pubkey. A row that fails
// either check is skipped (and logged) — a bad row never reaches the
// DB.
//
// The whole merge runs in one sql.Tx (mirroring persistRoster): the
// consumed-token G-Set is unioned first because it is the FK target
// of spanner_bolts.consumed_token_hash, then every verified bolt row
// is run through MergeBoltLWW.
func MergeSnapshot(ctx context.Context, q *db.Queries, sqlDB *sql.DB, snap Snapshot, founderPubkey ed25519.PublicKey) (merged int, err error) {
	if len(founderPubkey) != ed25519.PublicKeySize {
		return 0, fmt.Errorf("spanner crdt: founder pubkey is %d bytes, expected %d", len(founderPubkey), ed25519.PublicKeySize)
	}
	founderHex := hex.EncodeToString(founderPubkey)

	// Verify every bolt row up front. A row that fails verification is
	// dropped here and never enters the transaction.
	verified := make([]RosterRow, 0, len(snap.Roster))
	for _, rr := range snap.Roster {
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
		verified = append(verified, rr)
	}

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("spanner crdt: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after Commit is a no-op

	qtx := q.WithTx(tx)

	// Token G-Set union first — FK targets of spanner_bolts. The
	// G-Set merge is idempotent (ON CONFLICT DO NOTHING), so a
	// re-applied snapshot is a no-op.
	for _, tok := range snap.ConsumedTokens {
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

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("spanner crdt: commit: %w", err)
	}
	return merged, nil
}
