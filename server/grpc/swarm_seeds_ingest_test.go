// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm"
)

// TestIngestSwarmSeeds_EmptyPayloadRevokesAllSeeds pins the "drops its last
// seed" behavior: when a seeder removes its final seed file, the server must
// still auto-revoke that envoy's now-unreported seeds.
//
// The correctness here is subtle and easy to break, so it's worth a regression
// test. It relies on two facts conspiring:
//   - the agent's collect_all emits an empty/absent seed dir as an explicit
//     "swarm_seeds": null (it only omits a collector on error/panic/timeout,
//     not on a null value), and
//   - the server's len(seedsRaw)==0 early-return triggers only on an *absent*
//     key, not the 4-byte "null" literal — json.Unmarshal("null", …) is a
//     successful no-op that falls through to the unseed/auto-revoke loop.
//
// A future "skip null traits in collect_all" or "early-return on null seeds"
// change would silently reintroduce the stuck-active-seed bug; this catches it.
func TestIngestSwarmSeeds_EmptyPayloadRevokesAllSeeds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seedsRaw string
	}{
		{"null trait (empty or absent seed dir)", "null"},
		{"empty seeds array", `{"seeds":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := swarm.NewPeerTracker(60 * time.Second)
			fi := fleet.NewFleetIndex()
			fi.AddEnvoy(fleet.EnvoyState{ID: "envoy-a", Hostname: "annlap", IP: "10.0.0.2"}, nil)
			svc := &AgentService{peerTracker: tracker, fleetIndex: fi}

			sha1 := "aaa" + strings.Repeat("0", 61)
			sha2 := "bbb" + strings.Repeat("0", 61)
			tracker.RegisterSeed(sha1, swarm.SeedState{Label: "a", SeederID: "envoy-a", DistributedAt: time.Now()})
			tracker.RegisterSeed(sha2, swarm.SeedState{Label: "b", SeederID: "envoy-a", DistributedAt: time.Now()})
			if got := len(tracker.SeedsForEnvoy("envoy-a")); got != 2 {
				t.Fatalf("precondition: SeedsForEnvoy = %d, want 2", got)
			}

			// The envoy now reports no seeds — its last seed file was removed.
			svc.ingestSwarmSeeds("envoy-a", json.RawMessage(tc.seedsRaw))

			if got := tracker.SeedsForEnvoy("envoy-a"); len(got) != 0 {
				t.Errorf("after empty seeds report, SeedsForEnvoy = %v, want none (all auto-revoked)", got)
			}
		})
	}
}
