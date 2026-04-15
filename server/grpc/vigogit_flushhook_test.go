// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/vigogit"
)

// TestTraitsFlushHook_DrivesVigogitBuilder pins the wiring that was
// previously missing: when the TraitsBatcher flushes inline-reported
// traits, the vigogit Builder must receive the vigogit.sources[]
// entries so the ClaimTable can be populated. Without this, modern
// agents (which inline traits on check-in and never call the
// legacy ReportTraits RPC) silently leave the ClaimTable empty and
// no receiver ever gets a claim.
//
// This is a regression test for the "publisher works, bundle seeded,
// all receivers stay pending" symptom.
func TestTraitsFlushHook_DrivesVigogitBuilder(t *testing.T) {
	// Seed the fleet with the source envoy (danlap) and three
	// candidate receivers — all carrying a `dan` human user so they
	// qualify for vigogit sources owned by `dan`.
	fi := fleet.NewFleetIndex()
	seederID := addEnvoyWithUser(t, fi, "danlap-id", "danlap", "dan")
	addEnvoyWithUser(t, fi, "annlap-id", "annlap", "dan")
	addEnvoyWithUser(t, fi, "plex-id", "plex", "dan")
	addEnvoyWithUser(t, fi, "girlslaptop-id", "girlslaptop", "dan")

	ct := vigogit.NewClaimTable()
	builder := vigogit.NewBuilder(ct, fi)

	hook := buildTraitsFlushHook(fi, builder)

	// Simulate the agent's inline-traits payload on a check-in:
	// a vigogit snapshot containing one source labeled
	// vigogit:danlap:dan:vigo-web.
	traitsJSON, _ := json.Marshal(map[string]any{
		"vigogit": map[string]any{
			"active_sources": 1,
			"sources": []any{
				map[string]any{
					"label":       "vigogit:danlap:dan:vigo-web",
					"paused":      false,
					"max_mirrors": float64(5),
					"tags":        []any{"vigo"},
					"description": "test",
				},
			},
		},
	})

	hook(map[string]string{
		seederID: string(traitsJSON),
	})

	if ct.Len() != 1 {
		t.Fatalf("ClaimTable should have 1 source after flush, got %d", ct.Len())
	}
	state := ct.Get("vigogit:danlap:dan:vigo-web")
	if state == nil {
		t.Fatal("source missing from ClaimTable")
	}
	// The three non-seeder receivers should all be selected as
	// mirrors (pool size 3, max_mirrors 5 — all fit).
	if len(state.Mirrors) != 3 {
		t.Errorf("expected 3 mirrors, got %d: %+v", len(state.Mirrors), state.Mirrors)
	}
}

// TestTraitsFlushHook_RespectsExcludeEnvoys verifies that an
// exclude_envoys pattern carried through the inline-traits path
// actually filters the candidate pool. This is the end-to-end
// assertion for the sidecar-filter feature: parser → trait emit →
// flush hook → Builder → ClaimTable selection.
func TestTraitsFlushHook_RespectsExcludeEnvoys(t *testing.T) {
	fi := fleet.NewFleetIndex()
	seederID := addEnvoyWithUser(t, fi, "danlap-id", "danlap", "dan")
	addEnvoyWithUser(t, fi, "annlap-id", "annlap", "dan")
	addEnvoyWithUser(t, fi, "plex-id", "plex", "dan")
	addEnvoyWithUser(t, fi, "raspberrypi-id", "raspberrypi", "dan")

	ct := vigogit.NewClaimTable()
	builder := vigogit.NewBuilder(ct, fi)
	hook := buildTraitsFlushHook(fi, builder)

	traitsJSON, _ := json.Marshal(map[string]any{
		"vigogit": map[string]any{
			"sources": []any{
				map[string]any{
					"label":          "vigogit:danlap:dan:vigo-web",
					"paused":         false,
					"max_mirrors":    float64(5),
					"tags":           []any{},
					"description":    "",
					"exclude_envoys": []any{"raspberry*"},
				},
			},
		},
	})

	hook(map[string]string{seederID: string(traitsJSON)})

	state := ct.Get("vigogit:danlap:dan:vigo-web")
	if state == nil {
		t.Fatal("source missing")
	}
	// Candidate pool should be {annlap, plex} — raspberrypi
	// filtered out by the exclude_envoys pattern.
	if len(state.Mirrors) != 2 {
		t.Fatalf("expected 2 mirrors (annlap, plex), got %d: %+v", len(state.Mirrors), state.Mirrors)
	}
	for _, id := range state.Mirrors {
		if id == "raspberrypi-id" {
			t.Error("raspberrypi should have been excluded by exclude_envoys: [raspberry*]")
		}
	}
}

// addEnvoyWithUser inserts a minimal envoy into the fleet index with
// a single `users.human.0` trait so vigogit sees it as a mirror
// candidate for the given user. Returns the envoy ID.
func addEnvoyWithUser(t *testing.T, fi *fleet.FleetIndex, id, hostname, user string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	fi.AddEnvoy(fleet.EnvoyState{
		ID:                id,
		Hostname:          hostname,
		ConvergenceStatus: "no data",
		Traits: map[string]string{
			"users.human.0": user,
		},
	}, pub)
	return id
}
