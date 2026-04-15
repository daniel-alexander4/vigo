// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/vigorepo"
)

// TestTraitsFlushHook_DrivesVigorepoBuilder pins the wiring that was
// previously missing: when the TraitsBatcher flushes inline-reported
// traits, the vigorepo Builder must receive the vigorepo.sources[]
// entries so the ClaimTable can be populated. Without this, modern
// agents (which inline traits on check-in and never call the
// legacy ReportTraits RPC) silently leave the ClaimTable empty and
// no receiver ever gets a claim.
//
// This is a regression test for the "publisher works, bundle seeded,
// all receivers stay pending" symptom.
func TestTraitsFlushHook_DrivesVigorepoBuilder(t *testing.T) {
	// Seed the fleet with the source envoy (danlap) and three
	// candidate receivers — all carrying a `dan` human user so they
	// qualify for vigorepo sources owned by `dan`.
	fi := fleet.NewFleetIndex()
	seederID := addEnvoyWithUser(t, fi, "danlap-id", "danlap", "dan")
	addEnvoyWithUser(t, fi, "annlap-id", "annlap", "dan")
	addEnvoyWithUser(t, fi, "plex-id", "plex", "dan")
	addEnvoyWithUser(t, fi, "girlslaptop-id", "girlslaptop", "dan")

	ct := vigorepo.NewClaimTable()
	builder := vigorepo.NewBuilder(ct, fi)

	hook := buildTraitsFlushHook(fi, builder)

	// Simulate the agent's inline-traits payload on a check-in:
	// a vigorepo snapshot containing one source labeled
	// vigorepo:danlap:dan:vigo-web.
	traitsJSON, _ := json.Marshal(map[string]any{
		"vigorepo": map[string]any{
			"active_sources": 1,
			"sources": []any{
				map[string]any{
					"label":       "vigorepo:danlap:dan:vigo-web",
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
	state := ct.Get("vigorepo:danlap:dan:vigo-web")
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

	ct := vigorepo.NewClaimTable()
	builder := vigorepo.NewBuilder(ct, fi)
	hook := buildTraitsFlushHook(fi, builder)

	traitsJSON, _ := json.Marshal(map[string]any{
		"vigorepo": map[string]any{
			"sources": []any{
				map[string]any{
					"label":          "vigorepo:danlap:dan:vigo-web",
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

	state := ct.Get("vigorepo:danlap:dan:vigo-web")
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
// a single `users.human.0` trait so vigorepo sees it as a mirror
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

// addEnvoyWithUsers inserts an envoy with multiple `users.human.N`
// entries to match real production trait shape (e.g., annlap has
// `users.human: ["ann","dan"]` which flattens to users.human.0="ann"
// and users.human.1="dan"). The Builder filter must match on ANY
// entry, not just index 0.
func addEnvoyWithUsers(t *testing.T, fi *fleet.FleetIndex, id, hostname string, users ...string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	traits := make(map[string]string, len(users))
	for i, u := range users {
		traits[fmt.Sprintf("users.human.%d", i)] = u
	}
	fi.AddEnvoy(fleet.EnvoyState{
		ID:                id,
		Hostname:          hostname,
		ConvergenceStatus: "no data",
		Traits:            traits,
	}, pub)
	return id
}

// TestTraitsFlushHook_ReplaysProductionDanlapTrait replays the exact
// trait JSON captured from danlap's live vigorepo trait on 2026-04-14
// through the flush hook → Builder → ClaimTable pipeline, using real
// production-shape envoy trait maps (multi-user `users.human.N`
// arrays rather than single-entry fixtures). This is the smoking-gun
// diagnostic for the "receivers are stuck pending" symptom: if this
// test passes, the code logic is correct and the production failure
// is environmental (hook not wired, envoyID mismatch, data munged
// upstream of the hook, etc.). If it fails, we've got the bug in
// code and the test pinpoints exactly which envoy-ID the ClaimTable
// is dropping.
func TestTraitsFlushHook_ReplaysProductionDanlapTrait(t *testing.T) {
	fi := fleet.NewFleetIndex()

	// Real envoy IDs + hostnames pulled from the production fleet
	// index at the time of this test's creation.
	const (
		danlapID      = "02971dc4-68b5-4d4e-a205-4c3abb653e6d"
		plexID        = "00fd1dd0-b7dc-490f-a50d-2b26502c03e5"
		annlapID      = "d9bd46f0-2911-425b-b906-c9444128c2ac"
		girlslaptopID = "8ba797e3-07a4-4da7-ae87-a53b8347d65d"
		raspberrypiID = "3f2da33f-2246-4287-8af5-d0893fd56561"
	)

	// Real users.human arrays from live query output:
	//   girlslaptop: ["dan","anja","elena"]
	//   danlap:      ["dan","daniel.alexander","elena","anja"]
	//   plex:        ["dan"]
	//   annlap:      ["ann","dan"]
	//   raspberrypi: not queried — assume no dan user to emulate the
	//                worst case for the candidate pool filter.
	addEnvoyWithUsers(t, fi, danlapID, "danlap", "dan", "daniel.alexander", "elena", "anja")
	addEnvoyWithUsers(t, fi, plexID, "plex", "dan")
	addEnvoyWithUsers(t, fi, annlapID, "annlap", "ann", "dan")
	addEnvoyWithUsers(t, fi, girlslaptopID, "girlslaptop", "dan", "anja", "elena")
	addEnvoyWithUsers(t, fi, raspberrypiID, "raspberrypi", "pi")

	ct := vigorepo.NewClaimTable()
	builder := vigorepo.NewBuilder(ct, fi)
	hook := buildTraitsFlushHook(fi, builder)

	// Byte-for-byte replay of danlap's vigorepo trait as returned by
	// `vigocli traits livequery vigorepo --target danlap`. Wrapped in
	// the outer {"vigorepo": ...} envelope that the agent's full trait
	// JSON actually has at flush time.
	traitsJSON := []byte(`{
		"vigorepo": {
			"active_sources": 1,
			"paused_sources": 0,
			"publish_errors": [],
			"source_errors": [],
			"sources": [{
				"blob_sha": "c5281a0a978fc345811376afebd83480e9df71f36a6e6033244982fb08cf0054",
				"description": "Vigo marketing site — Go binary, independently versioned",
				"exclude_envoys": ["raspberry*"],
				"git_sha": "f43f6ef3eea29e3780d73aa2cee4a78193419a69",
				"label": "vigorepo:danlap:dan:vigo-web",
				"max_mirrors": 5,
				"paused": false,
				"tags": ["vigo", "marketing", "website"]
			}],
			"updated_at": 1776190715
		}
	}`)

	hook(map[string]string{danlapID: string(traitsJSON)})

	// --- Assertions ---

	// 1. ClaimTable should have exactly one source for the vigo-web
	//    label.
	if ct.Len() != 1 {
		t.Fatalf("ClaimTable has %d sources, want 1 — Builder.OnTraitReport did not upsert", ct.Len())
	}

	state := ct.Get("vigorepo:danlap:dan:vigo-web")
	if state == nil {
		t.Fatal("source vigorepo:danlap:dan:vigo-web missing from ClaimTable")
	}

	// 2. The sidecar state should preserve the exclude_envoys filter.
	if len(state.Sidecar.ExcludeEnvoys) != 1 || state.Sidecar.ExcludeEnvoys[0] != "raspberry*" {
		t.Errorf("Sidecar.ExcludeEnvoys = %+v, want [\"raspberry*\"]", state.Sidecar.ExcludeEnvoys)
	}

	// 3. Candidate pool filtering:
	//    - danlap excluded (seeder)
	//    - raspberrypi excluded by exclude_envoys pattern
	//    - raspberrypi ALSO excluded because it has no 'dan' user
	//    - annlap, plex, girlslaptop all qualify
	//    Expected mirrors: {annlap, plex, girlslaptop} — 3 total.
	//    max_mirrors clamped to [3, 10] per sidecar constants, so
	//    effective max_mirrors is 5. Pool size (3) < max_mirrors → all
	//    3 selected.
	if len(state.Mirrors) != 3 {
		t.Errorf("Mirrors count = %d, want 3: %+v", len(state.Mirrors), state.Mirrors)
	}
	mirrorSet := make(map[string]bool, len(state.Mirrors))
	for _, id := range state.Mirrors {
		mirrorSet[id] = true
	}
	for _, want := range []string{annlapID, plexID, girlslaptopID} {
		if !mirrorSet[want] {
			t.Errorf("Mirrors missing expected envoy %s", want)
		}
	}
	if mirrorSet[raspberrypiID] {
		t.Error("raspberrypi was selected as a mirror — exclude_envoys filter failed")
	}
	if mirrorSet[danlapID] {
		t.Error("danlap was selected as a mirror — seeder should never mirror its own source")
	}

	// 4. Claims(envoyID) should return the label for each mirror and
	//    an empty list for the seeder + the excluded raspberrypi.
	wantClaimsLabel := "vigorepo:danlap:dan:vigo-web"
	for _, id := range []string{annlapID, plexID, girlslaptopID} {
		claims := ct.Claims(id)
		if len(claims) != 1 || claims[0] != wantClaimsLabel {
			t.Errorf("Claims(%q) = %+v, want [%q] — CheckInResponse would deliver empty claims to this envoy",
				id, claims, wantClaimsLabel)
		}
	}
	for _, id := range []string{danlapID, raspberrypiID} {
		claims := ct.Claims(id)
		if len(claims) != 0 {
			t.Errorf("Claims(%q) = %+v, want empty", id, claims)
		}
	}

	// 5. Under-replicated flag should be true: UnderReplicated is
	//    defined as `candidates < sidecar.MaxMirrors` (not < MIN),
	//    so a 3-candidate pool under a max_mirrors=5 sidecar is
	//    technically under-replicated even though it meets
	//    MIN_MIRRORS. Operators see this via the
	//    vigorepo.under_replicated trait.
	if !state.UnderReplicated {
		t.Errorf("UnderReplicated = false, want true (pool size 3 < max_mirrors 5)")
	}
	if state.CandidateCount != 3 {
		t.Errorf("CandidateCount = %d, want 3", state.CandidateCount)
	}
}
