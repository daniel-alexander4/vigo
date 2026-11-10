// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package spanner

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// Spanner Slice 2-i-b — multicast Tier-1 fast-path tests.
//
// The unit tests below do NOT depend on the OS multicast stack: the
// sign/verify round-trip, RosterDigest stability/sensitivity, and the
// listener's decision logic (handleTier1Message) are all exercised
// directly. The one socket integration test (TestTier1SocketRoundTrip)
// skips gracefully when the environment cannot join a multicast group.

const testSpannerID = "test-spanner"

// ---------------------------------------------------------------------
// Tier-1 message sign / verify
// ---------------------------------------------------------------------

func TestDigestMessageSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	msg := &digestMessage{
		SpannerID:    testSpannerID,
		BoltPubkey:   hex.EncodeToString(pub),
		Tier1Digest: "deadbeef",
	}
	if err := signDigestMessage(priv, msg); err != nil {
		t.Fatalf("signDigestMessage: %v", err)
	}
	if len(msg.Signature) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes, want %d", len(msg.Signature), ed25519.SignatureSize)
	}
	if err := verifyDigestMessage(pub, msg); err != nil {
		t.Errorf("verifyDigestMessage on a freshly-signed message: %v", err)
	}
}

func TestDigestMessageVerifyRejectsTamper(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	base := func() *digestMessage {
		m := &digestMessage{
			SpannerID:    testSpannerID,
			BoltPubkey:   hex.EncodeToString(pub),
			Tier1Digest: "aaaa",
			SentAt:       1234,
		}
		if err := signDigestMessage(priv, m); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return m
	}

	t.Run("tampered digest", func(t *testing.T) {
		m := base()
		m.Tier1Digest = "bbbb"
		if err := verifyDigestMessage(pub, m); err == nil {
			t.Error("verify accepted a message with a tampered roster digest")
		}
	})
	t.Run("tampered spanner_id", func(t *testing.T) {
		m := base()
		m.SpannerID = "other-spanner"
		if err := verifyDigestMessage(pub, m); err == nil {
			t.Error("verify accepted a message with a tampered spanner_id")
		}
	})
	t.Run("tampered sent_at", func(t *testing.T) {
		m := base()
		m.SentAt = 9999
		if err := verifyDigestMessage(pub, m); err == nil {
			t.Error("verify accepted a message with a tampered sent_at")
		}
	})
	t.Run("corrupt signature", func(t *testing.T) {
		m := base()
		m.Signature[0] ^= 0xff
		if err := verifyDigestMessage(pub, m); err == nil {
			t.Error("verify accepted a message with a corrupt signature")
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		m := base()
		otherPub, _, _ := ed25519.GenerateKey(nil)
		if err := verifyDigestMessage(otherPub, m); err == nil {
			t.Error("verify accepted a message under the wrong pubkey")
		}
	})
}

func TestDatagramEncodeDecodeRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	msg := &digestMessage{
		SpannerID:    testSpannerID,
		BoltPubkey:   hex.EncodeToString(pub),
		Tier1Digest: "cafebabe",
		SentAt:       42,
	}
	if err := signDigestMessage(priv, msg); err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := encodeDatagram(msg)
	if err != nil {
		t.Fatalf("encodeDatagram: %v", err)
	}
	if string(raw[:len(multicastHeader)]) != multicastHeader {
		t.Errorf("datagram does not start with the %q header", multicastHeader)
	}
	got, err := decodeDatagram(raw)
	if err != nil {
		t.Fatalf("decodeDatagram: %v", err)
	}
	if got.Tier1Digest != msg.Tier1Digest || got.BoltPubkey != msg.BoltPubkey || got.SpannerID != msg.SpannerID {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, msg)
	}
	if err := verifyDigestMessage(pub, got); err != nil {
		t.Errorf("decoded message fails verification: %v", err)
	}
}

func TestDecodeDatagramRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"no header":        []byte(`{"spanner_id":"x"}`),
		"wrong header":     []byte("LOCKBOX-1\n{}"),
		"header no body":   []byte("SPANNER-1\n"),
		"undecodable json": []byte("SPANNER-1\nnot json"),
	}
	for name, raw := range cases {
		if _, err := decodeDatagram(raw); err == nil {
			t.Errorf("%s: decodeDatagram accepted a malformed datagram", name)
		}
	}
}

// ---------------------------------------------------------------------
// RosterDigest stability + sensitivity
// ---------------------------------------------------------------------

func boltRow(seed byte, hostname string, admittedAt int64) db.SpannerBolt {
	sig := make([]byte, ed25519.SignatureSize)
	for i := range sig {
		sig[i] = seed
	}
	return db.SpannerBolt{
		Pubkey:     hexKey(seed),
		Hostname:   hostname,
		AdmittedAt: admittedAt,
		AdmittedBy: hexKey(0x01),
		Signature:  sig,
	}
}

func TestTier1DigestStable(t *testing.T) {
	a := []db.SpannerBolt{
		boltRow(0x11, "b1", 100),
		boltRow(0x22, "b2", 200),
	}
	// Same rows in a different order — digest must be identical
	// because RosterDigest sorts by pubkey.
	b := []db.SpannerBolt{
		boltRow(0x22, "b2", 200),
		boltRow(0x11, "b1", 100),
	}
	if Tier1Digest(a, nil) != Tier1Digest(b, nil) {
		t.Error("RosterDigest is not order-independent")
	}
	// Same rows, same order, separately constructed — the digest must
	// match, proving it depends only on roster contents.
	c := []db.SpannerBolt{
		boltRow(0x11, "b1", 100),
		boltRow(0x22, "b2", 200),
	}
	if Tier1Digest(a, nil) != Tier1Digest(c, nil) {
		t.Error("RosterDigest is not deterministic for an unchanged roster")
	}
}

func TestTier1DigestSensitive(t *testing.T) {
	base := []db.SpannerBolt{boltRow(0x11, "b1", 100), boltRow(0x22, "b2", 200)}
	baseDigest := Tier1Digest(base, nil)

	t.Run("added row", func(t *testing.T) {
		more := append(append([]db.SpannerBolt{}, base...), boltRow(0x33, "b3", 300))
		if Tier1Digest(more, nil) == baseDigest {
			t.Error("adding a row did not change the digest")
		}
	})
	t.Run("mutated admitted_at", func(t *testing.T) {
		mut := []db.SpannerBolt{boltRow(0x11, "b1", 999), boltRow(0x22, "b2", 200)}
		if Tier1Digest(mut, nil) == baseDigest {
			t.Error("mutating admitted_at did not change the digest")
		}
	})
	t.Run("revoked row", func(t *testing.T) {
		rev := []db.SpannerBolt{boltRow(0x11, "b1", 100), boltRow(0x22, "b2", 200)}
		rev[0].RevokedAt = sql.NullInt64{Int64: 500, Valid: true}
		if Tier1Digest(rev, nil) == baseDigest {
			t.Error("revoking a row did not change the digest")
		}
	})
	t.Run("re-signed row", func(t *testing.T) {
		mut := []db.SpannerBolt{boltRow(0x11, "b1", 100), boltRow(0x22, "b2", 200)}
		mut[0].Signature[0] ^= 0xff
		if Tier1Digest(mut, nil) == baseDigest {
			t.Error("changing a signature did not change the digest")
		}
	})
	// Hostname is deliberately NOT part of the digest — admission
	// signature covers it, and a CRDT hostname change rides admitted_at.
	t.Run("hostname not in digest", func(t *testing.T) {
		mut := []db.SpannerBolt{boltRow(0x11, "renamed", 100), boltRow(0x22, "b2", 200)}
		if Tier1Digest(mut, nil) != baseDigest {
			t.Error("hostname unexpectedly contributes to the digest")
		}
	})
}

// signedUser builds a minimal db.User row with a non-nil signature so
// it counts toward the digest. Content is irrelevant — these tests
// exercise the hash-input shape, not signing semantics.
func signedUser(id, origin string, updatedAtMs int64, deletedAt int64, sigByte byte) db.User {
	u := db.User{
		ID:          id,
		UpdatedAtMs: updatedAtMs,
		OriginBolt:  origin,
		Signature:   []byte{sigByte, sigByte, sigByte, sigByte},
	}
	if deletedAt != 0 {
		u.DeletedAt = sql.NullInt64{Int64: deletedAt, Valid: true}
	}
	return u
}

func TestTier1DigestUsersStableAndSensitive(t *testing.T) {
	roster := []db.SpannerBolt{boltRow(0x11, "b1", 100)}
	baseUsers := []db.User{
		signedUser("u-alice", "ab12", 1000, 0, 0xaa),
		signedUser("u-bob", "ab12", 2000, 0, 0xbb),
	}
	baseDigest := Tier1Digest(roster, baseUsers)

	t.Run("order independent", func(t *testing.T) {
		swapped := []db.User{baseUsers[1], baseUsers[0]}
		if Tier1Digest(roster, swapped) != baseDigest {
			t.Error("digest is not user-order independent")
		}
	})
	t.Run("flipped updated_at_ms changes digest", func(t *testing.T) {
		mut := []db.User{baseUsers[0], baseUsers[1]}
		mut[0].UpdatedAtMs = 9999
		if Tier1Digest(roster, mut) == baseDigest {
			t.Error("updated_at_ms change did not affect the digest")
		}
	})
	t.Run("flipped tombstone changes digest", func(t *testing.T) {
		mut := []db.User{baseUsers[0], baseUsers[1]}
		mut[1].DeletedAt = sql.NullInt64{Int64: 5000, Valid: true}
		if Tier1Digest(roster, mut) == baseDigest {
			t.Error("deleted_at flip did not affect the digest")
		}
	})
	t.Run("flipped signature changes digest", func(t *testing.T) {
		mut := []db.User{baseUsers[0], baseUsers[1]}
		mut[0].Signature = []byte{0xff}
		if Tier1Digest(roster, mut) == baseDigest {
			t.Error("signature change did not affect the digest")
		}
	})
	t.Run("flipped origin_bolt changes digest", func(t *testing.T) {
		mut := []db.User{baseUsers[0], baseUsers[1]}
		mut[0].OriginBolt = "ffff"
		if Tier1Digest(roster, mut) == baseDigest {
			t.Error("origin_bolt change did not affect the digest")
		}
	})
}

func TestTier1DigestUnsignedUsersIgnored(t *testing.T) {
	roster := []db.SpannerBolt{boltRow(0x11, "b1", 100)}
	signed := []db.User{signedUser("u-alice", "ab12", 1000, 0, 0xaa)}
	baseDigest := Tier1Digest(roster, signed)

	// Add an unsigned (legacy / pre-replication) row. The digest must
	// be unchanged — those rows don't replicate via the snapshot
	// endpoint (Slice 2-iii-c filters them out) and shouldn't force
	// pointless Tier-2 pulls between bolts at different upgrade points.
	withUnsigned := append([]db.User{}, signed...)
	withUnsigned = append(withUnsigned, db.User{
		ID:          "u-legacy",
		UpdatedAtMs: 0,
		// OriginBolt = "", Signature = nil → unsigned legacy row
	})
	if Tier1Digest(roster, withUnsigned) != baseDigest {
		t.Error("unsigned user row unexpectedly contributed to digest")
	}
}

// TestTier1DigestUsersAndRosterContributeIndependently confirms a
// change to EITHER section flips the digest — the listener fast-path
// fires on any state drift, not just admissions drift.
func TestTier1DigestUsersAndRosterContributeIndependently(t *testing.T) {
	rosterA := []db.SpannerBolt{boltRow(0x11, "b1", 100)}
	rosterB := []db.SpannerBolt{boltRow(0x11, "b1", 200)} // different admitted_at
	users := []db.User{signedUser("u-alice", "ab12", 1000, 0, 0xaa)}

	if Tier1Digest(rosterA, users) == Tier1Digest(rosterB, users) {
		t.Error("roster change went undetected with same users")
	}
	usersB := []db.User{signedUser("u-alice", "ab12", 1000, 0, 0xbb)} // different sig
	if Tier1Digest(rosterA, users) == Tier1Digest(rosterA, usersB) {
		t.Error("user change went undetected with same roster")
	}
}

// ---------------------------------------------------------------------
// Listener decision logic — handleTier1Message
// ---------------------------------------------------------------------

// tier1Fixture is a Gossiper backed by a real migrated DB plus a
// recording stub for the pull target. The Gossiper's httpClient is
// pointed at a hostname the test inspects: handleTier1Message resolves
// the pull target from the LOCAL roster, so we assert on which
// hostname pullAndMerge was asked to dial.
type tier1Fixture struct {
	*crdtFixture
	g          *Gossiper
	pulledHost chan string
}

func newTier1Fixture(t *testing.T) *tier1Fixture {
	t.Helper()
	cf := newCRDTFixture(t)
	pulled := make(chan string, 8)

	// httpClient whose RoundTripper records the dialed host then fails
	// the request — pullAndMerge returns an error, which the listener
	// logs; the test only cares which host was targeted.
	client := &http.Client{Transport: &recordingTransport{pulled: pulled}}

	g := NewGossiper(
		cf.sqlDB, cf.q,
		cf.founderHex,      // ownPubkey
		"8443",             // httpsPort
		5*time.Minute,      // tier2 interval
		testSpannerID,      // spannerID
		cf.founderKey,      // signing key
		"224.0.0.45", 1534, // multicast group/port
		30*time.Second, // tier1 interval
		client,
	)
	return &tier1Fixture{crdtFixture: cf, g: g, pulledHost: pulled}
}

// TestNewGossiperRateLimitsTier1 guards the Tier-1 flood backstop: the
// listener rate-gates inbound datagrams before the sender-lookup DB read,
// so a spanner_id-knowing LAN flooder is reduced to cheap token-bucket
// rejects. Asserts the limiter is wired and that traffic past the burst is
// denied.
func TestNewGossiperRateLimitsTier1(t *testing.T) {
	g := NewGossiper(nil, nil, "pk", "8443", 0, testSpannerID, nil, "224.0.0.45", 1534, 0, http.DefaultClient)
	if g.tier1Limiter == nil {
		t.Fatal("NewGossiper must wire a Tier-1 rate limiter")
	}
	for i := 0; i < tier1RateBurst; i++ {
		if !g.tier1Limiter.Allow() {
			t.Fatalf("datagram %d within the burst was denied", i)
		}
	}
	if g.tier1Limiter.Allow() {
		t.Error("datagram past the burst should be rate-limited")
	}
}

// recordingTransport records the host of every request it sees on
// pulled, then fails the request. pullAndMerge surfaces the failure as
// a logged error; the test asserts only on the recorded host.
type recordingTransport struct {
	pulled chan<- string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.pulled <- req.URL.Hostname()
	return nil, fmt.Errorf("recordingTransport: connection intentionally refused")
}

// seedRoster merges a founder self-row + the given peer rows into the
// fixture DB so handleTier1Message can resolve them.
func (f *tier1Fixture) seedRoster(peers ...RosterRow) {
	f.t.Helper()
	rows := append([]RosterRow{f.founderSelfRow()}, peers...)
	f.merge(Snapshot{Roster: rows})
}

// localDigest is the fixture DB's current RosterDigest.
func (f *tier1Fixture) localDigest() string {
	f.t.Helper()
	roster, err := f.q.ListBolts(f.t.Context())
	if err != nil {
		f.t.Fatalf("ListBolts: %v", err)
	}
	return Tier1Digest(roster, nil)
}

// signedDigestFor builds a Tier-1 message from senderKey for the given
// roster digest + spanner id.
func signedDigestFor(t *testing.T, senderKey ed25519.PrivateKey, senderHex, spannerID, digest string) *digestMessage {
	t.Helper()
	m := &digestMessage{
		SpannerID:    spannerID,
		BoltPubkey:   senderHex,
		Tier1Digest: digest,
		SentAt:       time.Now().UnixMilli(),
	}
	if err := signDigestMessage(senderKey, m); err != nil {
		t.Fatalf("sign digest: %v", err)
	}
	return m
}

func (f *tier1Fixture) handle(msg *digestMessage) {
	f.g.handleTier1Message(f.t.Context(), msg, f.g.resolveFounderPubkey)
}

// assertNoPull fails if a pull was triggered within a short grace
// window.
func (f *tier1Fixture) assertNoPull() {
	f.t.Helper()
	select {
	case host := <-f.pulledHost:
		f.t.Errorf("expected no pull, but a pull was triggered for %q", host)
	case <-time.After(150 * time.Millisecond):
	}
}

// assertPull fails unless a pull was triggered for wantHost.
func (f *tier1Fixture) assertPull(wantHost string) {
	f.t.Helper()
	select {
	case host := <-f.pulledHost:
		if host != wantHost {
			f.t.Errorf("pull targeted %q, want %q (the local roster hostname)", host, wantHost)
		}
	case <-time.After(2 * time.Second):
		f.t.Errorf("expected a pull for %q, but none was triggered", wantHost)
	}
}

func TestTier1IgnoresNonMemberSender(t *testing.T) {
	f := newTier1Fixture(t)
	f.seedRoster() // founder only

	// A genuinely-signed message from a bolt that is NOT in the roster.
	strangerPub, strangerKey, _ := ed25519.GenerateKey(nil)
	strangerHex := hex.EncodeToString(strangerPub)
	msg := signedDigestFor(t, strangerKey, strangerHex, testSpannerID, "a-different-digest")

	f.handle(msg)
	f.assertNoPull()
}

func TestTier1IgnoresBadSignature(t *testing.T) {
	f := newTier1Fixture(t)
	// A real roster peer.
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "peer.example.com", 100, ""))

	msg := signedDigestFor(t, peerKey, peerHex, testSpannerID, "a-different-digest")
	msg.Signature[0] ^= 0xff // corrupt after signing

	f.handle(msg)
	f.assertNoPull()
}

func TestTier1IgnoresWrongSpannerID(t *testing.T) {
	f := newTier1Fixture(t)
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "peer.example.com", 100, ""))

	msg := signedDigestFor(t, peerKey, peerHex, "some-other-spanner", "a-different-digest")
	f.handle(msg)
	f.assertNoPull()
}

func TestTier1MatchingDigestNoPull(t *testing.T) {
	f := newTier1Fixture(t)
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "peer.example.com", 100, ""))

	// The peer announces THIS bolt's own current digest — a match.
	msg := signedDigestFor(t, peerKey, peerHex, testSpannerID, f.localDigest())
	f.handle(msg)
	f.assertNoPull()
}

func TestTier1MismatchTriggersPullToRosterHostname(t *testing.T) {
	f := newTier1Fixture(t)
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	// The peer's roster row carries the TRUSTED hostname.
	f.seedRoster(f.signedRow(peerHex, "trusted-peer.example.com", 100, ""))

	// The peer announces a digest different from ours → a pull fires.
	msg := signedDigestFor(t, peerKey, peerHex, testSpannerID, "a-different-digest")
	f.handle(msg)
	f.assertPull("trusted-peer.example.com")
}

func TestTier1PullTargetIsLocalRosterNotWire(t *testing.T) {
	// The digest message carries no hostname field at all — this test
	// confirms the pull target is the LOCAL roster row's hostname.
	f := newTier1Fixture(t)
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "real-peer.example.com", 100, ""))

	msg := signedDigestFor(t, peerKey, peerHex, testSpannerID, "mismatch")
	f.handle(msg)
	// Must dial the roster hostname, never anything attacker-supplied.
	f.assertPull("real-peer.example.com")
}

func TestTier1PerPeerDebounce(t *testing.T) {
	f := newTier1Fixture(t)
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "peer.example.com", 100, ""))

	msg := signedDigestFor(t, peerKey, peerHex, testSpannerID, "mismatch")
	// First mismatch → one pull.
	f.handle(msg)
	f.assertPull("peer.example.com")
	// Immediate second + third mismatch within the debounce window →
	// no further pull.
	f.handle(msg)
	f.handle(msg)
	f.assertNoPull()
}

func TestTier1IgnoresOwnEcho(t *testing.T) {
	f := newTier1Fixture(t)
	f.seedRoster()
	// A message whose BoltPubkey is this bolt itself — the multicast
	// echo. Must be ignored before any roster lookup.
	msg := signedDigestFor(t, f.founderKey, f.founderHex, testSpannerID, "whatever")
	f.handle(msg)
	f.assertNoPull()
}

// ---------------------------------------------------------------------
// Slice 2-ii-a — Tier-1 convergence-counter propagation into the
// observability cache.
// ---------------------------------------------------------------------

// TestTier1CountersUpdateObservabilityCache verifies that a valid,
// signed digest from a known roster member folds its convergence
// counters into the listener's observability cache — the 30s-fresh
// counter fast-path.
func TestTier1CountersUpdateObservabilityCache(t *testing.T) {
	f := newTier1Fixture(t)
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "peer.example.com", 2, ""))

	counters := ConvergenceCounters{Total: 42, Converged: 40, Failed: 2}
	msg := &digestMessage{
		SpannerID:    testSpannerID,
		BoltPubkey:   peerHex,
		Tier1Digest: f.localDigest(), // matching digest — no pull, but counters still cache
		Convergence:  counters,
		SentAt:       time.Now().UnixMilli(),
	}
	if err := signDigestMessage(peerKey, msg); err != nil {
		t.Fatalf("sign digest: %v", err)
	}

	f.handle(msg)

	got := f.g.observ.Get(peerHex)
	if got == nil {
		t.Fatal("Tier-1 digest did not create an observability-cache entry")
	}
	if got.Convergence != counters {
		t.Errorf("cached counters = %+v, want %+v", got.Convergence, counters)
	}
}

// TestTier1BadSignatureDoesNotCacheCounters confirms a digest whose
// signature does not verify never reaches the observability cache.
func TestTier1BadSignatureDoesNotCacheCounters(t *testing.T) {
	f := newTier1Fixture(t)
	peerPub, _, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "peer.example.com", 2, ""))

	// Sign with the WRONG key.
	_, wrongKey, _ := ed25519.GenerateKey(nil)
	msg := &digestMessage{
		SpannerID:    testSpannerID,
		BoltPubkey:   peerHex,
		Tier1Digest: "x",
		Convergence:  ConvergenceCounters{Total: 99},
		SentAt:       time.Now().UnixMilli(),
	}
	if err := signDigestMessage(wrongKey, msg); err != nil {
		t.Fatalf("sign: %v", err)
	}

	f.handle(msg)

	if f.g.observ.Get(peerHex) != nil {
		t.Error("a bad-signature Tier-1 digest cached counters")
	}
}

// ---------------------------------------------------------------------
// Best-effort socket integration test — skips when multicast is
// unavailable (CI/sandbox).
// ---------------------------------------------------------------------

func TestTier1SocketRoundTrip(t *testing.T) {
	const group, port = "224.0.0.45", 25334 // a high port to avoid privileged-port issues
	addr, err := multicastAddr(group, port)
	if err != nil {
		t.Fatalf("multicastAddr: %v", err)
	}

	listener, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		t.Skipf("environment cannot join a multicast group; skipping socket test: %v", err)
	}
	defer listener.Close()

	sender, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		t.Skipf("environment cannot dial the multicast group; skipping socket test: %v", err)
	}
	defer sender.Close()

	pub, priv, _ := ed25519.GenerateKey(nil)
	msg := &digestMessage{
		SpannerID:    testSpannerID,
		BoltPubkey:   hex.EncodeToString(pub),
		Tier1Digest: "socket-round-trip",
		SentAt:       time.Now().UnixMilli(),
	}
	if err := signDigestMessage(priv, msg); err != nil {
		t.Fatalf("sign: %v", err)
	}
	datagram, err := encodeDatagram(msg)
	if err != nil {
		t.Fatalf("encodeDatagram: %v", err)
	}

	if _, err := sender.Write(datagram); err != nil {
		t.Skipf("environment cannot write to the multicast group; skipping: %v", err)
	}

	buf := make([]byte, multicastReadBuf)
	_ = listener.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := listener.ReadFromUDP(buf)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Skip("multicast loopback delivery did not occur in this environment; skipping")
		}
		t.Fatalf("ReadFromUDP: %v", err)
	}

	got, err := decodeDatagram(buf[:n])
	if err != nil {
		t.Fatalf("decodeDatagram of the received datagram: %v", err)
	}
	if got.Tier1Digest != msg.Tier1Digest {
		t.Errorf("round-tripped digest = %q, want %q", got.Tier1Digest, msg.Tier1Digest)
	}
	if err := verifyDigestMessage(pub, got); err != nil {
		t.Errorf("received message fails verification: %v", err)
	}
}
