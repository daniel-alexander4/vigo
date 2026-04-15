// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"testing"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// TestBuildPeerVisualMap_Deterministic verifies that repeated calls with
// the same input produce identical (color, tier) assignments.
func TestBuildPeerVisualMap_Deterministic(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{"danlap": 3}},
		{Hostname: "annlap", ChunkSources: map[string]int{"plex": 2}},
		{Hostname: "danlap"},
	}

	colors1, tiers1 := buildPeerVisualMap(envoys)
	colors2, tiers2 := buildPeerVisualMap(envoys)

	for _, peer := range []string{"plex", "annlap", "danlap"} {
		if colors1[peer] != colors2[peer] {
			t.Errorf("color for %s is nondeterministic: %q vs %q", peer, colors1[peer], colors2[peer])
		}
		if tiers1[peer] != tiers2[peer] {
			t.Errorf("tier for %s is nondeterministic: %d vs %d", peer, tiers1[peer], tiers2[peer])
		}
	}
}

// TestBuildPeerVisualMap_AlphabeticSlots verifies that envoys get color
// slots in alphabetic order of hostname — annlap gets slot 0, danlap slot
// 1, plex slot 2. Stable across refreshes regardless of envoy status.
func TestBuildPeerVisualMap_AlphabeticSlots(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex"},
		{Hostname: "annlap"},
		{Hostname: "danlap"},
	}
	colors, tiers := buildPeerVisualMap(envoys)

	cases := []struct {
		hostname string
		slot     int
	}{
		{"annlap", 0},
		{"danlap", 1},
		{"plex", 2},
	}
	for _, c := range cases {
		wantColor := swarmVizColors[c.slot]
		if colors[c.hostname] != wantColor {
			t.Errorf("%s: got color %q (slot assignment wrong), want %q (slot %d)",
				c.hostname, colors[c.hostname], wantColor, c.slot)
		}
		wantTier := c.slot / 8
		if tiers[c.hostname] != wantTier {
			t.Errorf("%s: got tier %d, want %d", c.hostname, tiers[c.hostname], wantTier)
		}
	}
}

// TestBuildPeerVisualMap_TierBoundaries verifies the slot → tier mapping
// at the 8 and 16 boundaries: slot 7 is still tier 0, slot 8 is tier 1,
// slot 15 is tier 1, slot 16 is tier 2.
func TestBuildPeerVisualMap_TierBoundaries(t *testing.T) {
	// 17 hostnames sorted naturally as envoy-00 .. envoy-16, so slot N ==
	// envoy with suffix N.
	envoys := make([]swarm.EnvoySwarmStatus, 17)
	for i := range envoys {
		envoys[i].Hostname = fmt.Sprintf("envoy-%02d", i)
	}
	_, tiers := buildPeerVisualMap(envoys)

	cases := []struct {
		slot int
		tier int
	}{
		{0, 0}, {7, 0}, {8, 1}, {15, 1}, {16, 2},
	}
	for _, c := range cases {
		host := fmt.Sprintf("envoy-%02d", c.slot)
		if tiers[host] != c.tier {
			t.Errorf("slot %d (%s): got tier %d, want %d", c.slot, host, tiers[host], c.tier)
		}
	}
}

// TestBuildPeerVisualMap_CapAt24 verifies envoys beyond slot 23 are
// dropped from the returned maps (not assigned a fallback color).
func TestBuildPeerVisualMap_CapAt24(t *testing.T) {
	// 30 envoys — 6 past the cap.
	envoys := make([]swarm.EnvoySwarmStatus, 30)
	for i := range envoys {
		envoys[i].Hostname = fmt.Sprintf("envoy-%02d", i)
	}
	colors, tiers := buildPeerVisualMap(envoys)

	if len(colors) != 24 {
		t.Errorf("colors map has %d entries, want 24", len(colors))
	}
	if len(tiers) != 24 {
		t.Errorf("tiers map has %d entries, want 24", len(tiers))
	}
	// The first 24 alphabetically (envoy-00 .. envoy-23) should be present.
	for i := 0; i < 24; i++ {
		host := fmt.Sprintf("envoy-%02d", i)
		if _, ok := colors[host]; !ok {
			t.Errorf("envoy %s missing from colors map", host)
		}
	}
	// The tail (envoy-24 .. envoy-29) should be absent.
	for i := 24; i < 30; i++ {
		host := fmt.Sprintf("envoy-%02d", i)
		if _, ok := colors[host]; ok {
			t.Errorf("envoy %s present in colors map but should be past the 24-cap", host)
		}
	}
}

// TestBuildPeerVisualMap_ColorsFromPalette verifies every assigned color
// is one of the 24 declared palette entries.
func TestBuildPeerVisualMap_ColorsFromPalette(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "web01"}, {Hostname: "web02"}, {Hostname: "db01"},
	}
	colors, _ := buildPeerVisualMap(envoys)

	paletteSet := make(map[string]bool, len(swarmVizColors))
	for _, c := range swarmVizColors {
		paletteSet[c] = true
	}
	for peer, color := range colors {
		if !paletteSet[color] {
			t.Errorf("%s got color %q not in swarmVizColors", peer, color)
		}
	}
}

// TestBuildPeerVisualMap_EmptyInput verifies the empty case.
func TestBuildPeerVisualMap_EmptyInput(t *testing.T) {
	colors, tiers := buildPeerVisualMap(nil)
	if len(colors) != 0 || len(tiers) != 0 {
		t.Errorf("empty input should yield empty maps; got %d/%d", len(colors), len(tiers))
	}
}
