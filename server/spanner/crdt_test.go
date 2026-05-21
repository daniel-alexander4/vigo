// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"testing"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/testutil"
)

// crdtFixture is a founder identity plus a freshly migrated DB +
// query layer for CRDT-merge tests.
type crdtFixture struct {
	t          *testing.T
	sqlDB      *sql.DB
	q          *db.Queries
	founderPub ed25519.PublicKey
	founderKey ed25519.PrivateKey
	founderHex string
}

func newCRDTFixture(t *testing.T) *crdtFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate founder key: %v", err)
	}
	sqlDB := testutil.OpenMigratedTestDB(t)
	return &crdtFixture{
		t:          t,
		sqlDB:      sqlDB,
		q:          db.New(sqlDB),
		founderPub: pub,
		founderKey: priv,
		founderHex: hex.EncodeToString(pub),
	}
}

// signedRow builds a RosterRow admitted + signed by the founder.
func (f *crdtFixture) signedRow(pubkeyHex, hostname string, admittedAt int64, tokenHash string) RosterRow {
	f.t.Helper()
	arow := &AdmissionRow{
		Pubkey:            pubkeyHex,
		Hostname:          hostname,
		AdmittedAt:        admittedAt,
		AdmittedBy:        f.founderHex,
		ConsumedTokenHash: tokenHash,
		PatternClaims:     "[]",
	}
	sig, err := SignAdmissionRow(f.founderKey, arow)
	if err != nil {
		f.t.Fatalf("sign row %s: %v", pubkeyHex, err)
	}
	return RosterRow{
		Pubkey:            pubkeyHex,
		Hostname:          hostname,
		AdmittedAt:        admittedAt,
		AdmittedBy:        f.founderHex,
		ConsumedTokenHash: tokenHash,
		PatternClaims:     "[]",
		Signature:         sig,
	}
}

// revokedRow is signedRow with revoked_* set. The admission signature
// still covers only the identity/grant fields (revoked_* excluded),
// so it remains valid.
func (f *crdtFixture) revokedRow(pubkeyHex, hostname string, admittedAt, revokedAt int64) RosterRow {
	f.t.Helper()
	rr := f.signedRow(pubkeyHex, hostname, admittedAt, "")
	rr.RevokedAt = revokedAt
	rr.RevokedBy = f.founderHex
	return rr
}

// founderSelfRow is the founder's own self-admit row (pubkey ==
// admitted_by, no consumed token).
func (f *crdtFixture) founderSelfRow() RosterRow {
	return f.signedRow(f.founderHex, "founder.example.com", 1, "")
}

// merge applies a snapshot and returns the mutated-row count.
func (f *crdtFixture) merge(snap Snapshot) int {
	f.t.Helper()
	n, err := MergeSnapshot(f.t.Context(), f.q, f.sqlDB, snap, f.founderPub)
	if err != nil {
		f.t.Fatalf("MergeSnapshot: %v", err)
	}
	return n
}

// rosterState returns the local roster keyed by pubkey for comparison.
func (f *crdtFixture) rosterState() map[string]db.SpannerBolt {
	f.t.Helper()
	rows, err := f.q.ListBolts(f.t.Context())
	if err != nil {
		f.t.Fatalf("ListBolts: %v", err)
	}
	out := make(map[string]db.SpannerBolt, len(rows))
	for _, r := range rows {
		out[r.Pubkey] = r
	}
	return out
}

// sameRosterState fails if two roster maps differ on the fields the
// LWW merge governs.
func sameRosterState(t *testing.T, a, b map[string]db.SpannerBolt) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("roster sizes differ: %d != %d", len(a), len(b))
	}
	for pk, ra := range a {
		rb, ok := b[pk]
		if !ok {
			t.Fatalf("pubkey %s missing from second roster", pk)
		}
		if ra.AdmittedAt != rb.AdmittedAt {
			t.Errorf("%s admitted_at differs: %d != %d", pk, ra.AdmittedAt, rb.AdmittedAt)
		}
		if ra.Hostname != rb.Hostname {
			t.Errorf("%s hostname differs: %q != %q", pk, ra.Hostname, rb.Hostname)
		}
		if ra.RevokedAt.Valid != rb.RevokedAt.Valid || ra.RevokedAt.Int64 != rb.RevokedAt.Int64 {
			t.Errorf("%s revoked_at differs: %v != %v", pk, ra.RevokedAt, rb.RevokedAt)
		}
	}
}

// hexKey returns a deterministic 64-hex-char pubkey for test bolts.
func hexKey(seed byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed
	}
	return hex.EncodeToString(raw)
}

// TestMergeSnapshotOrderIndependent applies snapshots A then B to one
// DB and B then A to a second; the end state must be identical —
// CRDT commutativity.
func TestMergeSnapshotOrderIndependent(t *testing.T) {
	fa := newCRDTFixture(t)
	// fb must share fa's founder identity so both verify against the
	// same key and converge on the same founder self-admit row.
	fb := &crdtFixture{
		t: t, sqlDB: testutil.OpenMigratedTestDB(t),
		founderPub: fa.founderPub, founderKey: fa.founderKey, founderHex: fa.founderHex,
	}
	fb.q = db.New(fb.sqlDB)

	bolt1 := hexKey(0x11)
	bolt2 := hexKey(0x22)

	// Snapshot A: founder + bolt1. Snapshot B: founder + bolt1 at a
	// later admitted_at (LWW winner) + bolt2.
	snapA := Snapshot{Roster: []RosterRow{
		fa.founderSelfRow(),
		fa.signedRow(bolt1, "b1.example.com", 100, ""),
	}}
	snapB := Snapshot{Roster: []RosterRow{
		fa.founderSelfRow(),
		fa.signedRow(bolt1, "b1-renamed.example.com", 200, ""),
		fa.signedRow(bolt2, "b2.example.com", 150, ""),
	}}

	// fa: A then B.
	fa.merge(snapA)
	fa.merge(snapB)
	// fb: B then A.
	fb.merge(snapB)
	fb.merge(snapA)

	sameRosterState(t, fa.rosterState(), fb.rosterState())

	// Both must hold bolt1's later (admitted_at=200) hostname.
	if got := fa.rosterState()[bolt1].Hostname; got != "b1-renamed.example.com" {
		t.Errorf("fa bolt1 hostname = %q, want the LWW winner b1-renamed.example.com", got)
	}
	if got := fb.rosterState()[bolt1].Hostname; got != "b1-renamed.example.com" {
		t.Errorf("fb bolt1 hostname = %q, want the LWW winner b1-renamed.example.com", got)
	}
}

// TestMergeSnapshotRevokeWins verifies a revoked row never returns to
// live, regardless of merge order.
func TestMergeSnapshotRevokeWins(t *testing.T) {
	f := newCRDTFixture(t)
	bolt1 := hexKey(0x33)

	live := f.signedRow(bolt1, "b1.example.com", 100, "")
	revoked := f.revokedRow(bolt1, "b1.example.com", 100, 100)

	// Merge live, then revoked → must end revoked.
	f.merge(Snapshot{Roster: []RosterRow{f.founderSelfRow(), live}})
	f.merge(Snapshot{Roster: []RosterRow{revoked}})
	if !f.rosterState()[bolt1].RevokedAt.Valid {
		t.Fatal("bolt1 should be revoked after merging the revoked row")
	}

	// Re-merge the live row → revoke must still win.
	f.merge(Snapshot{Roster: []RosterRow{live}})
	if !f.rosterState()[bolt1].RevokedAt.Valid {
		t.Error("revoke-wins violated: live row resurrected a revoked bolt")
	}
}

// TestMergeSnapshotRejectsBadSignature confirms a row with a tampered
// (or absent) signature is skipped and never merged.
func TestMergeSnapshotRejectsBadSignature(t *testing.T) {
	f := newCRDTFixture(t)
	bolt1 := hexKey(0x44)
	bolt2 := hexKey(0x55)

	tampered := f.signedRow(bolt1, "b1.example.com", 100, "")
	tampered.Signature[0] ^= 0xff // corrupt

	missing := f.signedRow(bolt2, "b2.example.com", 100, "")
	missing.Signature = nil // absent

	good := f.founderSelfRow()

	merged := f.merge(Snapshot{Roster: []RosterRow{good, tampered, missing}})
	state := f.rosterState()
	if _, ok := state[bolt1]; ok {
		t.Error("a row with a tampered signature was merged")
	}
	if _, ok := state[bolt2]; ok {
		t.Error("a row with a missing signature was merged")
	}
	if _, ok := state[f.founderHex]; !ok {
		t.Error("the valid founder row should have merged")
	}
	if merged != 1 {
		t.Errorf("merged count = %d, want 1 (only the founder row is valid)", merged)
	}
}

// TestMergeSnapshotRejectsNonFounderAdmitter confirms a row whose
// admitted_by is not the founder is skipped — in 2-i-a every
// admission is founder-issued.
func TestMergeSnapshotRejectsNonFounderAdmitter(t *testing.T) {
	f := newCRDTFixture(t)
	bolt1 := hexKey(0x66)

	// A row genuinely signed by a different bolt, claiming that bolt
	// as admitter. VerifyAdmissionRow against the founder would fail
	// anyway, but the admitted_by check rejects it before that.
	otherPub, otherKey, _ := ed25519.GenerateKey(nil)
	otherHex := hex.EncodeToString(otherPub)
	arow := &AdmissionRow{
		Pubkey: bolt1, Hostname: "b1.example.com", AdmittedAt: 100,
		AdmittedBy: otherHex, PatternClaims: "[]",
	}
	sig, _ := SignAdmissionRow(otherKey, arow)
	rr := RosterRow{
		Pubkey: bolt1, Hostname: "b1.example.com", AdmittedAt: 100,
		AdmittedBy: otherHex, PatternClaims: "[]", Signature: sig,
	}

	merged := f.merge(Snapshot{Roster: []RosterRow{f.founderSelfRow(), rr}})
	if _, ok := f.rosterState()[bolt1]; ok {
		t.Error("a non-founder-admitted row was merged")
	}
	if merged != 1 {
		t.Errorf("merged = %d, want 1 (only the founder self-row)", merged)
	}
}

// TestMergeSnapshotConsumedTokenIdempotent confirms re-applying a
// snapshot that carries a consumed token is a no-op G-Set union.
func TestMergeSnapshotConsumedTokenIdempotent(t *testing.T) {
	f := newCRDTFixture(t)
	bolt1 := hexKey(0x77)
	tokenHash := TokenHash("a-test-join-token")

	snap := Snapshot{
		Roster: []RosterRow{
			f.founderSelfRow(),
			f.signedRow(bolt1, "b1.example.com", 100, tokenHash),
		},
		ConsumedTokens: []ConsumedToken{{
			TokenHash: tokenHash, ConsumedAt: 90, ConsumedBy: f.founderHex,
			AdmittedPubkey: bolt1, ExpiresAt: 999,
		}},
	}

	f.merge(snap)
	before := f.rosterState()
	// Re-apply the identical snapshot — must change nothing.
	if n := f.merge(snap); n != 0 {
		t.Errorf("re-applying an identical snapshot mutated %d rows, want 0", n)
	}
	sameRosterState(t, before, f.rosterState())

	// The token row must exist exactly once and be readable.
	tok, err := f.q.GetConsumedToken(f.t.Context(), tokenHash)
	if err != nil {
		t.Fatalf("GetConsumedToken: %v", err)
	}
	if tok.AdmittedPubkey != bolt1 {
		t.Errorf("consumed token admitted_pubkey = %q, want %q", tok.AdmittedPubkey, bolt1)
	}
}

// TestMergeSnapshotCrossPeerReplay is the convergence case: two
// snapshots admit different bolts but reference the SAME consumed
// token (a replay across peers). After merging both, the roster must
// converge — the G-Set holds the token once and both bolt rows land.
func TestMergeSnapshotCrossPeerReplay(t *testing.T) {
	f := newCRDTFixture(t)
	boltX := hexKey(0x88)
	boltY := hexKey(0x99)
	tokenHash := TokenHash("shared-token")

	snapX := Snapshot{
		Roster: []RosterRow{
			f.founderSelfRow(),
			f.signedRow(boltX, "x.example.com", 100, tokenHash),
		},
		ConsumedTokens: []ConsumedToken{{
			TokenHash: tokenHash, ConsumedAt: 90, ConsumedBy: f.founderHex,
			AdmittedPubkey: boltX, ExpiresAt: 999,
		}},
	}
	snapY := Snapshot{
		Roster: []RosterRow{
			f.founderSelfRow(),
			f.signedRow(boltY, "y.example.com", 110, tokenHash),
		},
		ConsumedTokens: []ConsumedToken{{
			TokenHash: tokenHash, ConsumedAt: 90, ConsumedBy: f.founderHex,
			AdmittedPubkey: boltY, ExpiresAt: 999,
		}},
	}

	f.merge(snapX)
	f.merge(snapY)
	state := f.rosterState()
	if len(state) != 3 {
		t.Fatalf("roster has %d rows, want 3 (founder + X + Y)", len(state))
	}
	if _, ok := state[boltX]; !ok {
		t.Error("boltX missing after cross-peer merge")
	}
	if _, ok := state[boltY]; !ok {
		t.Error("boltY missing after cross-peer merge")
	}
	// The G-Set holds the token once; the first-writer's admitted_pubkey
	// stays (ON CONFLICT DO NOTHING).
	tok, err := f.q.GetConsumedToken(f.t.Context(), tokenHash)
	if err != nil {
		t.Fatalf("GetConsumedToken: %v", err)
	}
	if tok.AdmittedPubkey != boltX {
		t.Errorf("token admitted_pubkey = %q, want %q (first writer wins the G-Set)", tok.AdmittedPubkey, boltX)
	}
}

// TestFounderPubkeyResolution checks FounderPubkey identifies the
// self-admit row and errors when none exists.
func TestFounderPubkeyResolution(t *testing.T) {
	f := newCRDTFixture(t)

	// Empty roster → error.
	if _, err := FounderPubkey(nil); err == nil {
		t.Error("FounderPubkey on an empty roster should error")
	}

	// Merge a founder + a token-admitted bolt.
	bolt1 := hexKey(0xaa)
	f.merge(Snapshot{Roster: []RosterRow{
		f.founderSelfRow(),
		f.signedRow(bolt1, "b1.example.com", 100, ""),
	}})
	roster, err := f.q.ListBolts(f.t.Context())
	if err != nil {
		t.Fatalf("ListBolts: %v", err)
	}
	got, err := FounderPubkey(roster)
	if err != nil {
		t.Fatalf("FounderPubkey: %v", err)
	}
	if hex.EncodeToString(got) != f.founderHex {
		t.Errorf("FounderPubkey = %s, want %s", hex.EncodeToString(got), f.founderHex)
	}
}
