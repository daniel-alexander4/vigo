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

// TestSynthesizeSourceMap_ProportionalDistribution verifies that aggregate
// ChunkSources are distributed proportionally across synthetic chunk indices.
func TestSynthesizeSourceMap_ProportionalDistribution(t *testing.T) {
	sources := map[string]int{
		"danlap":      75,
		"girlslaptop": 25,
	}
	result := synthesizeSourceMap(sources, 100)

	if len(result) != 100 {
		t.Fatalf("expected 100 entries, got %d", len(result))
	}

	// Count per source.
	counts := map[string]int{}
	for _, src := range result {
		counts[src]++
	}
	if counts["danlap"] != 75 {
		t.Errorf("danlap: got %d chunks, want 75", counts["danlap"])
	}
	if counts["girlslaptop"] != 25 {
		t.Errorf("girlslaptop: got %d chunks, want 25", counts["girlslaptop"])
	}
}

// TestSynthesizeSourceMap_SkipsCached verifies the "cached" pseudo-source
// is excluded from synthesis — it's not a real peer.
func TestSynthesizeSourceMap_SkipsCached(t *testing.T) {
	sources := map[string]int{
		"danlap": 80,
		"cached": 20,
	}
	result := synthesizeSourceMap(sources, 100)

	for idx, src := range result {
		if src == "cached" {
			t.Errorf("chunk %d attributed to 'cached', should be excluded", idx)
		}
	}
	// Only 80 chunks attributed (danlap); indices 80-99 have no entry.
	counts := map[string]int{}
	for _, src := range result {
		counts[src]++
	}
	if counts["danlap"] != 80 {
		t.Errorf("danlap: got %d, want 80", counts["danlap"])
	}
}

// TestSynthesizeSourceMap_Empty verifies nil return for empty inputs.
func TestSynthesizeSourceMap_Empty(t *testing.T) {
	if result := synthesizeSourceMap(nil, 100); result != nil {
		t.Errorf("nil sources should return nil, got %d entries", len(result))
	}
	if result := synthesizeSourceMap(map[string]int{"a": 1}, 0); result != nil {
		t.Errorf("zero totalChunks should return nil, got %d entries", len(result))
	}
}

// TestSynthesizeSourceMap_DeterministicOrder verifies repeated calls produce
// the same chunk→source mapping (sorted by count desc, then name).
func TestSynthesizeSourceMap_DeterministicOrder(t *testing.T) {
	sources := map[string]int{
		"alpha": 30,
		"beta":  30,
		"gamma": 40,
	}
	r1 := synthesizeSourceMap(sources, 100)
	r2 := synthesizeSourceMap(sources, 100)

	for i := 0; i < 100; i++ {
		if r1[i] != r2[i] {
			t.Errorf("chunk %d: %q vs %q (nondeterministic)", i, r1[i], r2[i])
			break
		}
	}

	// gamma (40) first, then alpha (30, alphabetically before beta), then beta (30).
	if r1[0] != "gamma" {
		t.Errorf("chunk 0: got %q, want gamma (highest count)", r1[0])
	}
	if r1[40] != "alpha" {
		t.Errorf("chunk 40: got %q, want alpha (tie-break alphabetical)", r1[40])
	}
	if r1[70] != "beta" {
		t.Errorf("chunk 70: got %q, want beta", r1[70])
	}
}

// TestBucketChunksForEnvoy_CachedStatus verifies that envoys with status
// "cached" render all buckets as "complete" (not "missing").
func TestBucketChunksForEnvoy_CachedStatus(t *testing.T) {
	envoy := swarm.EnvoySwarmStatus{
		Hostname:     "girlslaptop",
		Status:       "cached",
		ChunksHave:   100,
		ChunksNeed:   100,
		ChunkSources: map[string]int{"danlap": 90, "plex": 10},
		// No ChunkDetails — cached blob, no per-chunk data.
	}

	buckets := bucketChunksForEnvoy(envoy, 100, 10, 10)

	for i, b := range buckets {
		if b.Status != "complete" {
			t.Errorf("bucket %d: status %q, want complete", i, b.Status)
		}
		if b.Source == "" {
			t.Errorf("bucket %d: source empty, expected synthesized source", i)
		}
	}
}

// TestBucketChunksForEnvoy_SynthesizedSources verifies that when ChunkDetails
// is empty but ChunkSources is populated, the strip chart gets proportional
// source coloring from the aggregate data.
func TestBucketChunksForEnvoy_SynthesizedSources(t *testing.T) {
	envoy := swarm.EnvoySwarmStatus{
		Hostname:     "plex",
		Status:       "complete",
		ChunksHave:   100,
		ChunksNeed:   100,
		ChunkSources: map[string]int{"danlap": 70, "girlslaptop": 30},
		// No ChunkDetails.
	}

	buckets := bucketChunksForEnvoy(envoy, 100, 10, 10)

	// Collect dominant sources across all buckets.
	sourceBuckets := map[string]int{}
	for _, b := range buckets {
		sourceBuckets[b.Source]++
	}

	// danlap should dominate (7 of 10 buckets), girlslaptop gets 3.
	if sourceBuckets["danlap"] != 7 {
		t.Errorf("danlap dominant in %d buckets, want 7", sourceBuckets["danlap"])
	}
	if sourceBuckets["girlslaptop"] != 3 {
		t.Errorf("girlslaptop dominant in %d buckets, want 3", sourceBuckets["girlslaptop"])
	}
}

// TestBucketChunksForEnvoy_NoSourcesNoSynthesis verifies that envoys with
// neither ChunkDetails nor ChunkSources still get empty source (grey).
func TestBucketChunksForEnvoy_NoSourcesNoSynthesis(t *testing.T) {
	envoy := swarm.EnvoySwarmStatus{
		Hostname:   "server",
		Status:     "source",
		ChunksHave: 100,
		ChunksNeed: 100,
		// No ChunkDetails, no ChunkSources.
	}

	buckets := bucketChunksForEnvoy(envoy, 100, 10, 10)

	for i, b := range buckets {
		if b.Status != "complete" {
			t.Errorf("bucket %d: status %q, want complete (source envoy)", i, b.Status)
		}
		// Source should be empty — the seeder has no download sources.
		if b.Source != "" {
			t.Errorf("bucket %d: source %q, want empty for seeder", i, b.Source)
		}
	}
}

// TestBuildChunkMatrix_ShapeAndCounts verifies that the matrix has one row
// and column per peer in peerColors (alphabetic) and that cell counts match
// the ChunkSources input.
func TestBuildChunkMatrix_ShapeAndCounts(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{"danlap": 60, "annlap": 40}},
		{Hostname: "annlap", ChunkSources: map[string]int{"danlap": 100}},
		{Hostname: "danlap", ChunkSources: map[string]int{}},
	}
	peerColors := map[string]string{
		"annlap": "#f9e2af",
		"danlap": "#f38ba8",
		"plex":   "#a6e3a1",
	}

	m := buildChunkMatrix(envoys, peerColors)

	wantPeers := []string{"annlap", "danlap", "plex"}
	if len(m.Peers) != len(wantPeers) {
		t.Fatalf("Peers len = %d, want %d", len(m.Peers), len(wantPeers))
	}
	for i, p := range wantPeers {
		if m.Peers[i] != p {
			t.Errorf("Peers[%d] = %q, want %q", i, m.Peers[i], p)
		}
	}

	// Expected cell counts by [source][target]:
	//   danlap → annlap: 100
	//   danlap → plex:    60
	//   annlap → plex:    40
	cases := []struct {
		source, target string
		want           int
	}{
		{"danlap", "annlap", 100},
		{"danlap", "plex", 60},
		{"annlap", "plex", 40},
		{"annlap", "danlap", 0},
		{"plex", "annlap", 0},
	}
	idx := func(n string) int {
		for i, p := range m.Peers {
			if p == n {
				return i
			}
		}
		t.Fatalf("peer %q not in matrix", n)
		return -1
	}
	for _, tc := range cases {
		got := m.Rows[idx(tc.source)].Cells[idx(tc.target)].Chunks
		if got != tc.want {
			t.Errorf("cell[%s][%s] = %d, want %d", tc.source, tc.target, got, tc.want)
		}
	}

	if m.MaxCount != 100 {
		t.Errorf("MaxCount = %d, want 100", m.MaxCount)
	}
	if m.Empty {
		t.Error("Empty = true, want false (matrix has transfers)")
	}
}

// TestBuildChunkMatrix_DiagonalIsZero verifies that self-transfers from
// malformed input never populate the diagonal — a peer serving itself is a
// nonsense edge and would mislead the viewer if rendered as a real cell.
func TestBuildChunkMatrix_DiagonalIsZero(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{"plex": 50, "danlap": 50}},
	}
	peerColors := map[string]string{
		"plex":   "#a6e3a1",
		"danlap": "#f38ba8",
	}

	m := buildChunkMatrix(envoys, peerColors)

	for i := range m.Peers {
		if m.Rows[i].Cells[i].Chunks != 0 {
			t.Errorf("diagonal cell [%d][%d] = %d, want 0", i, i, m.Rows[i].Cells[i].Chunks)
		}
		if m.Rows[i].Cells[i].BgColor != "" {
			t.Errorf("diagonal cell [%d][%d] BgColor = %q, want empty", i, i, m.Rows[i].Cells[i].BgColor)
		}
	}
}

// TestBuildChunkMatrix_SkipsCachedAndOffPalette verifies that the "cached"
// pseudo-source and any peer missing from peerColors (e.g. a cross-subnet
// seeder beyond the 24-slot palette cap) are dropped from the matrix.
func TestBuildChunkMatrix_SkipsCachedAndOffPalette(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{
			"cached":       30,
			"offpalette01": 20,
			"danlap":       50,
		}},
	}
	peerColors := map[string]string{
		"plex":   "#a6e3a1",
		"danlap": "#f38ba8",
	}

	m := buildChunkMatrix(envoys, peerColors)

	// Only danlap → plex = 50 should survive.
	if m.MaxCount != 50 {
		t.Errorf("MaxCount = %d, want 50 (cached/off-palette dropped)", m.MaxCount)
	}
	for _, src := range m.Peers {
		if src == "cached" || src == "offpalette01" {
			t.Errorf("matrix unexpectedly includes %q", src)
		}
	}
}

// TestBuildChunkMatrix_EmptyWhenNoTransfers verifies that a matrix built
// from envoys with no ChunkSources is marked Empty so the template can
// show a "no transfers yet" message instead of a blank grid.
func TestBuildChunkMatrix_EmptyWhenNoTransfers(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex"},
		{Hostname: "danlap"},
	}
	peerColors := map[string]string{
		"plex":   "#a6e3a1",
		"danlap": "#f38ba8",
	}

	m := buildChunkMatrix(envoys, peerColors)

	if !m.Empty {
		t.Error("Empty = false, want true (no transfers)")
	}
	if m.MaxCount != 0 {
		t.Errorf("MaxCount = %d, want 0", m.MaxCount)
	}
}

// TestCellAlpha_Bounds verifies that cellAlpha stays in [0.25, 1.0] across
// the expected count range so lightly-populated cells still render visibly
// and the heaviest cell reaches full saturation.
func TestCellAlpha_Bounds(t *testing.T) {
	cases := []struct {
		count, max int
		wantLow    float64
		wantHigh   float64
	}{
		{0, 100, 0, 0},       // zero count → zero alpha (no BgColor emitted)
		{1, 100, 0.25, 0.26}, // minimum populated alpha
		{50, 100, 0.62, 0.63},
		{100, 100, 1.0, 1.0}, // full saturation at the max
	}
	for _, tc := range cases {
		got := cellAlpha(tc.count, tc.max)
		if got < tc.wantLow-0.01 || got > tc.wantHigh+0.01 {
			t.Errorf("cellAlpha(%d, %d) = %.3f, want in [%.2f, %.2f]", tc.count, tc.max, got, tc.wantLow, tc.wantHigh)
		}
	}
}

// TestRgbaFromHex_RoundTrip verifies that rgbaFromHex parses a valid hex
// color and emits a CSS rgba() string with the requested alpha.
func TestRgbaFromHex_RoundTrip(t *testing.T) {
	got := rgbaFromHex("#a6e3a1", 0.5)
	want := "rgba(166,227,161,0.500)"
	if got != want {
		t.Errorf("rgbaFromHex = %q, want %q", got, want)
	}
}
