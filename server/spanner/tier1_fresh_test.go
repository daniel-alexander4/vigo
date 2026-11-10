// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package spanner

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"
)

func signedDigestAt(t *testing.T, key ed25519.PrivateKey, senderHex, digest string, at time.Time) *digestMessage {
	t.Helper()
	m := &digestMessage{SpannerID: testSpannerID, BoltPubkey: senderHex, Tier1Digest: digest, SentAt: at.UnixMilli()}
	if err := signDigestMessage(key, m); err != nil {
		t.Fatalf("sign digest: %v", err)
	}
	return m
}

// A validly signed but stale (or future-dated) Tier-1 digest is ignored: a
// captured datagram replayed later must not force a pull.
func TestTier1IgnoresStaleOrFutureDigest(t *testing.T) {
	for name, at := range map[string]time.Time{
		"stale":  time.Now().Add(-10 * time.Minute),
		"future": time.Now().Add(10 * time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			f := newTier1Fixture(t)
			peerPub, peerKey, _ := ed25519.GenerateKey(nil)
			peerHex := hex.EncodeToString(peerPub)
			f.seedRoster(f.signedRow(peerHex, "peer.example.com", 100, ""))
			f.handle(signedDigestAt(t, peerKey, peerHex, "a-different-digest", at))
			f.assertNoPull()
		})
	}
}

// A digest older than one already accepted from the same sender is a replay.
func TestTier1IgnoresReplayOlderThanLastAccepted(t *testing.T) {
	f := newTier1Fixture(t)
	peerPub, peerKey, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)
	f.seedRoster(f.signedRow(peerHex, "peer.example.com", 100, ""))

	now := time.Now()
	// The newer message matches local state (no pull) and sets the mark.
	f.handle(signedDigestAt(t, peerKey, peerHex, f.localDigest(), now))
	f.assertNoPull()
	// An older captured message with a mismatching digest is refused.
	f.handle(signedDigestAt(t, peerKey, peerHex, "a-different-digest", now.Add(-30*time.Second)))
	f.assertNoPull()
}
