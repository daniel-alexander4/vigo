// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package lockboxmesh

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

// FailedFiles aggregation: a user's failed-artifact count is summed across
// that user's reporting envoys (each .failed artifact is local to one envoy,
// so there's no cross-envoy dedup — unlike synced files).
func TestBuildUserSummary_FailedFilesSummed(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Unix() - 5

	rows := []envRow{
		{hostname: "a", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", FailedFileCount: 2}},
		}},
		{hostname: "b", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users:     []userTrait{{User: "dan", FailedFileCount: 3}},
		}},
		// A not-reporting envoy contributes nothing.
		{hostname: "c", reported: false},
	}
	s := buildUserSummary("dan", rows, now)
	if s.FailedFiles != 5 {
		t.Errorf("FailedFiles = %d, want 5 (2+3 across reporting envoys)", s.FailedFiles)
	}
	// Per-envoy presence carries its own count.
	for _, ep := range s.Envoys {
		switch ep.Hostname {
		case "a":
			if ep.FailedFileCount != 2 {
				t.Errorf("envoy a FailedFileCount = %d, want 2", ep.FailedFileCount)
			}
		case "b":
			if ep.FailedFileCount != 3 {
				t.Errorf("envoy b FailedFileCount = %d, want 3", ep.FailedFileCount)
			}
		}
	}
}
