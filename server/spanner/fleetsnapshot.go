// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
)

// Spanner Slice 2-ii-a — the Tier-2 fleet-observability snapshot
// (ADR-026 + the spanner-redesign plan). This is the payload served by
// GET /api/v1/spanner/fleet-snapshot and pulled by the Gossiper on the
// same Tier-2 cadence as the admissions /snapshot.
//
// It is deliberately SEPARATE from the admissions Snapshot (crdt.go):
// different consistency model (per-bolt facts, last-writer-wins — see
// observ.go) and different size profile (a 5K-envoy roster vs a small
// admissions roster). The two payloads are never merged.
//
// Every gossiped spanner payload is signed; this is no exception. The
// publishing bolt signs the canonical content with its Ed25519
// identity; a pulling peer verifies the signature against that bolt's
// roster pubkey before caching. Canonical-marshal discipline mirrors
// admission.go: build a content-only value with the signature field
// cleared, marshal deterministically, ed25519.Sign / Verify.

// FleetSnapshot is one bolt's full fleet-observability state — its
// envoy roster plus its convergence counters — signed by the bolt's
// identity. BoltPubkey identifies the publisher (hex-encoded raw
// Ed25519 pubkey); ServedAt is the publishing bolt's clock at build
// time, the last-writer-wins ordering key in ObservabilityCache.
type FleetSnapshot struct {
	BoltPubkey  string              `json:"bolt_pubkey"`
	Envoys      []EnvoyLite         `json:"envoys"`
	Convergence ConvergenceCounters `json:"convergence"`
	ServedAt    int64               `json:"served_at"` // ms-since-epoch
	Signature   []byte              `json:"signature"`
}

// fleetSnapshotContent is the canonical content of a FleetSnapshot —
// every field except Signature. Marshalled deterministically for
// sign/verify.
type fleetSnapshotContent struct {
	BoltPubkey  string              `json:"bolt_pubkey"`
	Envoys      []EnvoyLite         `json:"envoys"`
	Convergence ConvergenceCounters `json:"convergence"`
	ServedAt    int64               `json:"served_at"`
}

// marshalFleetSnapshotContent serializes a FleetSnapshot's content
// fields (Signature cleared) for signing/verification.
func marshalFleetSnapshotContent(s *FleetSnapshot) ([]byte, error) {
	return json.Marshal(fleetSnapshotContent{
		BoltPubkey:  s.BoltPubkey,
		Envoys:      s.Envoys,
		Convergence: s.Convergence,
		ServedAt:    s.ServedAt,
	})
}

// SignFleetSnapshot signs a FleetSnapshot's canonical content with the
// publishing bolt's signing key, filling its Signature field.
func SignFleetSnapshot(signingKey ed25519.PrivateKey, s *FleetSnapshot) error {
	if len(signingKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("fleet snapshot sign: signing key is %d bytes, expected %d", len(signingKey), ed25519.PrivateKeySize)
	}
	content, err := marshalFleetSnapshotContent(s)
	if err != nil {
		return fmt.Errorf("fleet snapshot sign: canonical marshal: %w", err)
	}
	s.Signature = ed25519.Sign(signingKey, content)
	return nil
}

// VerifyFleetSnapshot checks s's signature against boltPubkey (the raw
// 32-byte Ed25519 pubkey of the publishing bolt). Returns nil on a
// valid signature. A bad signature → the caller drops the snapshot and
// does not cache it.
func VerifyFleetSnapshot(boltPubkey ed25519.PublicKey, s *FleetSnapshot) error {
	if len(boltPubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("fleet snapshot verify: bolt pubkey is %d bytes, expected %d", len(boltPubkey), ed25519.PublicKeySize)
	}
	if len(s.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("fleet snapshot verify: signature is %d bytes, expected %d", len(s.Signature), ed25519.SignatureSize)
	}
	content, err := marshalFleetSnapshotContent(s)
	if err != nil {
		return fmt.Errorf("fleet snapshot verify: canonical marshal: %w", err)
	}
	if !ed25519.Verify(boltPubkey, content, s.Signature) {
		return fmt.Errorf("fleet snapshot verify: ed25519 verify failed")
	}
	return nil
}
