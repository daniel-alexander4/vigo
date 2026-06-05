// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm"
)

// TestIngestSwarmTraits_EvictsRevokedBlob_WhenAgentReportsEmptyInventory
// is a regression test for the "stuck in revoking" bug: an agent that
// has finished evicting its last blob reports an empty swarm.blobs
// array, and the server must still call MarkEvicted so the per-envoy
// status flips from "revoking" to "revoked". Before the fix,
// ingestSwarmTraits short-circuited on empty blobs+downloading and
// skipped the eviction-detection loop entirely — envoys that had
// nothing else cached would stay in "revoking" forever.
func TestIngestSwarmTraits_EvictsRevokedBlob_WhenAgentReportsEmptyInventory(t *testing.T) {
	tracker := swarm.NewPeerTracker(60 * time.Second)
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:       "envoy-a",
		Hostname: "annlap",
		IP:       "10.0.0.2",
	}, nil)
	svc := &AgentService{
		peerTracker: tracker,
		fleetIndex:  fi,
	}

	sha := "aaa" + strings.Repeat("0", 61)

	// Seed registered, envoy-a had the blob complete, then the
	// operator revoked the seed fleet-wide. At this point the tracker
	// record for envoy-a should transition complete → revoking on
	// the next Status() call, and stay there until MarkEvicted fires.
	tracker.RegisterSeed(sha, swarm.SeedState{
		Label:         "big.iso",
		SeederID:      "envoy-seeder",
		Target:        "*",
		DistributedAt: time.Now(),
	})
	tracker.ReportComplete("envoy-a", sha, "10.0.0.2:1531")
	tracker.RevokeSeed(sha)

	pre := tracker.Status(sha, 1, "envoy-seeder")
	found := false
	for _, s := range pre {
		if s.Hostname == "envoy-a" {
			found = true
			if s.Status != "revoking" {
				t.Fatalf("pre-ingest: envoy-a should be revoking, got %q", s.Status)
			}
		}
	}
	if !found {
		t.Fatal("pre-ingest: envoy-a missing from Status result")
	}

	// Agent's next trait flush: swarm.blobs is empty (the revoked
	// blob was its only entry), downloading is empty. This is the
	// exact shape that used to trip the early-return bug. ingestSwarmTraits
	// now takes the pre-parsed "swarm" sub-tree, not the full traits payload.
	emptyInventory := []byte(`{"blobs":[],"downloading":{}}`)
	svc.ingestSwarmTraits("envoy-a", emptyInventory)

	// After ingestion, envoy-a should have flipped to "revoked".
	post := tracker.Status(sha, 1, "envoy-seeder")
	for _, s := range post {
		if s.Hostname == "envoy-a" {
			if s.Status != "revoked" {
				t.Errorf("post-ingest: envoy-a should be revoked, got %q "+
					"(the empty-inventory early-return bug regressed)", s.Status)
			}
			return
		}
	}
	t.Fatal("post-ingest: envoy-a missing from Status result")
}

// TestIngestSwarmTraits_EmptyInventoryNoOpWithoutRevokedSeeds confirms
// that the removed early-return was a legitimate no-op on the happy
// path: when there's nothing to report and nothing to evict, the
// function should do no work (no panics, no spurious tracker state).
func TestIngestSwarmTraits_EmptyInventoryNoOpWithoutRevokedSeeds(t *testing.T) {
	tracker := swarm.NewPeerTracker(60 * time.Second)
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:       "envoy-a",
		Hostname: "plex",
		IP:       "10.0.0.3",
	}, nil)
	svc := &AgentService{
		peerTracker: tracker,
		fleetIndex:  fi,
	}

	svc.ingestSwarmTraits("envoy-a", []byte(`{"blobs":[],"downloading":{}}`))

	// Tracker should still be pristine — no seeds, no peers.
	if got := len(tracker.ListSeeds()); got != 0 {
		t.Errorf("expected 0 seeds, got %d", got)
	}
}
