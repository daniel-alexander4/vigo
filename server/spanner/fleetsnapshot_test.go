// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"
)

// Spanner Slice 2-ii-a — fleet-snapshot sign/verify tests + the
// gossiper's verify-then-cache decision (acceptFleetSnapshot).

func newFleetSnapshot(boltPubkey string) *FleetSnapshot {
	return &FleetSnapshot{
		BoltPubkey:  boltPubkey,
		Envoys:      []EnvoyLite{{ID: "e1", Hostname: "h1"}, {ID: "e2", Hostname: "h2"}},
		Convergence: ConvergenceCounters{Total: 2, Converged: 1, Failed: 1},
		ServedAt:    time.Now().UnixMilli(),
	}
}

func TestFleetSnapshotSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	snap := newFleetSnapshot(hex.EncodeToString(pub))
	if err := SignFleetSnapshot(priv, snap); err != nil {
		t.Fatalf("SignFleetSnapshot: %v", err)
	}
	if len(snap.Signature) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes, want %d", len(snap.Signature), ed25519.SignatureSize)
	}
	if err := VerifyFleetSnapshot(pub, snap); err != nil {
		t.Errorf("VerifyFleetSnapshot on a freshly-signed snapshot: %v", err)
	}
}

func TestFleetSnapshotVerifyRejectsTamper(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	base := newFleetSnapshot(hex.EncodeToString(pub))
	if err := SignFleetSnapshot(priv, base); err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Tamper with the envoy roster — signature must no longer verify.
	tampered := *base
	tampered.Envoys = append([]EnvoyLite{}, base.Envoys...)
	tampered.Envoys[0].Hostname = "evil-host"
	if err := VerifyFleetSnapshot(pub, &tampered); err == nil {
		t.Error("VerifyFleetSnapshot accepted a tampered envoy roster")
	}

	// Tamper with the counters.
	tampered2 := *base
	tampered2.Convergence.Total = 999
	if err := VerifyFleetSnapshot(pub, &tampered2); err == nil {
		t.Error("VerifyFleetSnapshot accepted tampered counters")
	}
}

func TestFleetSnapshotVerifyRejectsWrongKey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	snap := newFleetSnapshot(hex.EncodeToString(pub))
	if err := SignFleetSnapshot(priv, snap); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyFleetSnapshot(otherPub, snap); err == nil {
		t.Error("VerifyFleetSnapshot accepted a signature from the wrong key")
	}
}

// TestAcceptFleetSnapshotCachesVerified verifies the gossiper's
// verify-then-cache path: a correctly signed snapshot from a peer
// lands in the observability cache; a bad-signature snapshot does not.
func TestAcceptFleetSnapshotCachesVerified(t *testing.T) {
	peerPub, peerPriv, _ := ed25519.GenerateKey(nil)
	peerHex := hex.EncodeToString(peerPub)

	g := &Gossiper{observ: NewObservabilityCache()}

	// Good snapshot — verifies, caches.
	good := newFleetSnapshot(peerHex)
	if err := SignFleetSnapshot(peerPriv, good); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := g.acceptFleetSnapshot(peerHex, good); err != nil {
		t.Fatalf("acceptFleetSnapshot rejected a valid snapshot: %v", err)
	}
	if cached := g.observ.Get(peerHex); cached == nil || len(cached.Envoys) != 2 {
		t.Fatalf("valid snapshot was not cached: %+v", cached)
	}

	// Bad-signature snapshot — must be dropped, not cached.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	otherHex := hex.EncodeToString(otherPub)
	bad := newFleetSnapshot(otherHex)
	bad.Signature = good.Signature // signature does not match otherPub
	if err := g.acceptFleetSnapshot(otherHex, bad); err == nil {
		t.Error("acceptFleetSnapshot accepted a bad-signature snapshot")
	}
	if g.observ.Get(otherHex) != nil {
		t.Error("a bad-signature snapshot was cached")
	}
}

func TestAcceptFleetSnapshotRejectsBadPubkeyHex(t *testing.T) {
	g := &Gossiper{observ: NewObservabilityCache()}
	snap := newFleetSnapshot("not-hex")
	if err := g.acceptFleetSnapshot("not-hex", snap); err == nil {
		t.Error("acceptFleetSnapshot accepted a non-hex peer pubkey")
	}
}
