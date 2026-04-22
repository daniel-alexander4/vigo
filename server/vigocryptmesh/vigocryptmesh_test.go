// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigocryptmesh

import (
	"testing"
	"time"
)

// Recipient-drift detection: when two reporting envoys in the same
// user's mesh have different peer sets, the aggregator must flag
// RecipientDrift=true. Symmetric recipient sets produce
// RecipientDrift=false.
func TestBuildUserSummary_RecipientDrift(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Unix() - 5

	peersA := []peerTrait{
		{Hostname: "b", Pubkey: "age1b", SignPubkey: "ed25519:b"},
		{Hostname: "c", Pubkey: "age1c", SignPubkey: "ed25519:c"},
	}
	peersB := []peerTrait{
		{Hostname: "a", Pubkey: "age1a", SignPubkey: "ed25519:a"},
		{Hostname: "c", Pubkey: "age1c", SignPubkey: "ed25519:c"},
	}

	rows := []envRow{
		{hostname: "a", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", Peers: peersA}},
		}},
		{hostname: "b", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", Peers: peersB}},
		}},
	}
	s := buildUserSummary("dan", rows, now)
	if !s.RecipientDrift {
		t.Error("expected RecipientDrift=true when peers differ")
	}

	// Same peers reported on both envoys → no drift.
	rows2 := []envRow{
		{hostname: "a", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", Peers: peersA}},
		}},
		{hostname: "b", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", Peers: peersA}},
		}},
	}
	s2 := buildUserSummary("dan", rows2, now)
	if s2.RecipientDrift {
		t.Error("expected RecipientDrift=false when peers match")
	}
}

// Session-unlocked per-envoy aggregation: ActiveSessions lists only
// envoys whose trait reported session_unlocked=true. Totals.UnlockedSessions
// is the global count.
func TestBuildUserSummary_ActiveSessions(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Unix() - 5

	rows := []envRow{
		{hostname: "a", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", SessionUnlocked: true}},
		}},
		{hostname: "b", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", SessionUnlocked: false}},
		}},
		{hostname: "c", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", SessionUnlocked: true}},
		}},
	}
	s := buildUserSummary("dan", rows, now)
	if len(s.ActiveSessions) != 2 {
		t.Fatalf("expected 2 active sessions, got %d (%v)", len(s.ActiveSessions), s.ActiveSessions)
	}
	if s.ActiveSessions[0] != "a" || s.ActiveSessions[1] != "c" {
		t.Errorf("expected a+c active, got %v", s.ActiveSessions)
	}
}

// recipientsDigest must be deterministic across peer-table
// insertion order.
func TestRecipientsDigestStable(t *testing.T) {
	a := []peerTrait{
		{Hostname: "alpha", Pubkey: "age1alpha", SignPubkey: "ed25519:alpha"},
		{Hostname: "beta", Pubkey: "age1beta", SignPubkey: "ed25519:beta"},
	}
	b := []peerTrait{
		{Hostname: "beta", Pubkey: "age1beta", SignPubkey: "ed25519:beta"},
		{Hostname: "alpha", Pubkey: "age1alpha", SignPubkey: "ed25519:alpha"},
	}
	if recipientsDigest(a) != recipientsDigest(b) {
		t.Error("digest must be stable regardless of input order")
	}
}

// Tombstones + their KnownOn hostnames must come out deterministically
// sorted so the UI renders the same order every reload regardless of
// envoy arrival order upstream.
func TestBuildUserSummary_TombstonesSorted(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Unix() - 10

	rows := []envRow{
		{hostname: "plex", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users: []userTrait{{
				User: "dan",
				Tombstones: []tombstoneTrait{
					{Name: "z-later.txt", DeletionTime: fresh - 50},
					{Name: "a-first.txt", DeletionTime: fresh - 100},
				},
			}},
		}},
		{hostname: "annlap", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users: []userTrait{{
				User: "dan",
				Tombstones: []tombstoneTrait{
					{Name: "a-first.txt", DeletionTime: fresh - 100},
					{Name: "z-later.txt", DeletionTime: fresh - 50},
				},
			}},
		}},
	}

	summary := buildUserSummary("dan", rows, now)
	if len(summary.Tombstones) != 2 {
		t.Fatalf("want 2 tombstones, got %d", len(summary.Tombstones))
	}
	if summary.Tombstones[0].Name != "a-first.txt" {
		t.Errorf("tombstones not sorted by name: %+v", summary.Tombstones)
	}
	for _, tb := range summary.Tombstones {
		if len(tb.KnownOn) != 2 || tb.KnownOn[0] != "annlap" || tb.KnownOn[1] != "plex" {
			t.Errorf("%s KnownOn not alpha-sorted: %v", tb.Name, tb.KnownOn)
		}
	}
}
