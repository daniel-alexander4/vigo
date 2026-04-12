// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"testing"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// TestBuildPeerVisualMap_DeterministicAndComplete verifies that every peer
// gets a triple (color, shape, dash) and the assignment is stable across
// calls for the same input.
func TestBuildPeerVisualMap_DeterministicAndComplete(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{"danlap": 3}},
		{Hostname: "annlap", ChunkSources: map[string]int{"plex": 2}},
		{Hostname: "danlap"},
	}

	colors1, shapes1, dashes1 := buildPeerVisualMap(envoys)
	colors2, shapes2, dashes2 := buildPeerVisualMap(envoys)

	// Determinism: two calls with the same input give identical output.
	for _, peer := range []string{"plex", "annlap", "danlap"} {
		if colors1[peer] != colors2[peer] {
			t.Errorf("color for %s is nondeterministic: %q vs %q", peer, colors1[peer], colors2[peer])
		}
		if shapes1[peer] != shapes2[peer] {
			t.Errorf("shape for %s is nondeterministic: %q vs %q", peer, shapes1[peer], shapes2[peer])
		}
		if dashes1[peer] != dashes2[peer] {
			t.Errorf("dash for %s is nondeterministic: %q vs %q", peer, dashes1[peer], dashes2[peer])
		}
	}

	// Completeness: every peer mentioned (as hostname OR as chunk source)
	// has an entry in all three maps.
	for _, peer := range []string{"plex", "annlap", "danlap"} {
		if _, ok := colors1[peer]; !ok {
			t.Errorf("color map missing peer %s", peer)
		}
		if _, ok := shapes1[peer]; !ok {
			t.Errorf("shape map missing peer %s", peer)
		}
		if _, ok := dashes1[peer]; !ok {
			t.Errorf("dash map missing peer %s", peer)
		}
	}
}

// TestBuildPeerVisualMap_AssignmentsFromPalette verifies every assigned
// value comes from the declared palette — no stray strings or defaults.
func TestBuildPeerVisualMap_AssignmentsFromPalette(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "web01"}, {Hostname: "web02"}, {Hostname: "db01"},
	}
	colors, shapes, dashes := buildPeerVisualMap(envoys)

	colorSet := toStringSet(VizColorPalette)
	shapeSet := toStringSet(VizShapePalette)
	dashSet := toStringSet(VizDashPalette)

	for peer, color := range colors {
		if !colorSet[color] {
			t.Errorf("%s got color %q not in VizColorPalette", peer, color)
		}
	}
	for peer, shape := range shapes {
		if !shapeSet[shape] {
			t.Errorf("%s got shape %q not in VizShapePalette", peer, shape)
		}
	}
	for peer, dash := range dashes {
		if !dashSet[dash] {
			t.Errorf("%s got dash %q not in VizDashPalette", peer, dash)
		}
	}
}

// TestBuildPeerVisualMap_DimensionsAreIndependent verifies the FNV
// bit-range trick produces uncorrelated dimensions: two peers that share
// a color should NOT be guaranteed to share a shape. Across a moderate
// set of peers, we expect multiple distinct (color, shape) combinations.
func TestBuildPeerVisualMap_DimensionsAreIndependent(t *testing.T) {
	// 30 synthetic hostnames — comfortably past the 14-color palette size.
	envoys := make([]swarm.EnvoySwarmStatus, 30)
	for i := 0; i < 30; i++ {
		envoys[i] = swarm.EnvoySwarmStatus{Hostname: hostnameN(i)}
	}
	colors, shapes, _ := buildPeerVisualMap(envoys)

	// Group peers by color; inside each group, count distinct shapes.
	// With truly independent dimensions, color-collision groups should
	// usually have multiple shapes represented. We assert the weaker
	// property that across all collisions there's at least one case
	// where two peers with the same color have different shapes — this
	// rules out a full correlation bug.
	colorToShapes := make(map[string]map[string]bool)
	for peer, c := range colors {
		if colorToShapes[c] == nil {
			colorToShapes[c] = make(map[string]bool)
		}
		colorToShapes[c][shapes[peer]] = true
	}

	foundMultiShape := false
	for _, shapeSet := range colorToShapes {
		if len(shapeSet) > 1 {
			foundMultiShape = true
			break
		}
	}
	if !foundMultiShape {
		t.Errorf("no color-collision group had multiple shapes — dimensions appear correlated (30 peers, %d distinct colors)", len(colorToShapes))
	}
}

// TestBuildPeerVisualMap_EmptyInput verifies the empty case.
func TestBuildPeerVisualMap_EmptyInput(t *testing.T) {
	colors, shapes, dashes := buildPeerVisualMap(nil)
	if len(colors) != 0 || len(shapes) != 0 || len(dashes) != 0 {
		t.Errorf("empty input should yield empty maps; got %d/%d/%d", len(colors), len(shapes), len(dashes))
	}
}

// toStringSet converts a slice of strings to a set for O(1) membership checks.
func toStringSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// hostnameN returns a deterministic synthetic hostname for tests.
func hostnameN(n int) string {
	return "envoy-" + string(rune('a'+n%26)) + string(rune('a'+(n/26)%26))
}
