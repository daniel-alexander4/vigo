// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/swarm"
)

// TestBuildPeerVisualMap_Deterministic verifies that repeated calls with
// the same input produce identical (color, tier) assignments.
func TestBuildPeerVisualMap_Deterministic(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{"danlap": 3}},
		{Hostname: "annlap", ChunkSources: map[string]int{"plex": 2}},
		{Hostname: "danlap"},
	}

	colors1, tiers1 := buildPeerVisualMap(envoys, nil)
	colors2, tiers2 := buildPeerVisualMap(envoys, nil)

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
	colors, tiers := buildPeerVisualMap(envoys, nil)

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
	_, tiers := buildPeerVisualMap(envoys, nil)

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
	colors, tiers := buildPeerVisualMap(envoys, nil)

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
	colors, _ := buildPeerVisualMap(envoys, nil)

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
	colors, tiers := buildPeerVisualMap(nil, nil)
	if len(colors) != 0 || len(tiers) != 0 {
		t.Errorf("empty input should yield empty maps; got %d/%d", len(colors), len(tiers))
	}
}

// TestBuildPeerVisualMap_TagOverride_NamedColor verifies that a
// `viz.color=<name>` tag pins the envoy to the named hue regardless
// of its hash-based slot. Operator can keep "plex is always blue"
// across redeploys.
func TestBuildPeerVisualMap_TagOverride_NamedColor(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex"},
		{Hostname: "annlap"},
		{Hostname: "danlap"},
	}
	tagLookup := func(host string) []string {
		switch host {
		case "plex":
			return []string{"site:home", "viz.color=blue"}
		case "annlap":
			return []string{"viz.color=mauve"}
		}
		return nil
	}
	colors, tiers := buildPeerVisualMap(envoys, tagLookup)

	if got, want := colors["plex"], vizNamedColors["blue"]; got != want {
		t.Errorf("plex: got %q, want %q (viz.color=blue override)", got, want)
	}
	if got, want := colors["annlap"], vizNamedColors["mauve"]; got != want {
		t.Errorf("annlap: got %q, want %q (viz.color=mauve override)", got, want)
	}
	// Untagged envoy keeps its hash-assigned slot color.
	if got, want := colors["danlap"], swarmVizColors[1]; got != want {
		t.Errorf("danlap (untagged): got %q, want %q (slot 1 color)", got, want)
	}
	// Tier left at slot's natural value — color override doesn't move
	// the envoy off its slot index.
	if tiers["plex"] != 0 || tiers["annlap"] != 0 || tiers["danlap"] != 0 {
		t.Errorf("tiers should be unchanged by color override: %+v", tiers)
	}
}

// TestBuildPeerVisualMap_TagOverride_HexPassthrough verifies a
// `viz.color=#rrggbb` tag is accepted as a literal hex code so an
// operator can pin to a custom hue without waiting for it to land
// in the named-color table.
func TestBuildPeerVisualMap_TagOverride_HexPassthrough(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{{Hostname: "plex"}}
	tagLookup := func(host string) []string {
		return []string{"viz.color=#abcdef"}
	}
	colors, _ := buildPeerVisualMap(envoys, tagLookup)
	if colors["plex"] != "#abcdef" {
		t.Errorf("hex-code override: got %q, want #abcdef", colors["plex"])
	}
}

// TestBuildPeerVisualMap_TagOverride_BadValueIgnored verifies that
// garbage `viz.color=…` values silently fall through to the default
// slot assignment rather than producing a broken color string in
// the rendered viz.
func TestBuildPeerVisualMap_TagOverride_BadValueIgnored(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{{Hostname: "plex"}}
	cases := []string{
		"viz.color=",
		"viz.color=invalid",
		"viz.color=red ish",
		"viz.color=12345",
		"viz.color=#zz",
		"VIZ.COLOR=  ",
	}
	for _, tag := range cases {
		t.Run(tag, func(t *testing.T) {
			tagLookup := func(host string) []string { return []string{tag} }
			colors, _ := buildPeerVisualMap(envoys, tagLookup)
			if colors["plex"] != swarmVizColors[0] {
				t.Errorf("bad tag %q: got %q, want default slot 0 color %q",
					tag, colors["plex"], swarmVizColors[0])
			}
		})
	}
}

// TestBuildPeerVisualMap_NilTagLookup verifies the legacy hash-only
// behavior is preserved when no tag lookup is wired (test path or
// pre-tag-pinning callers).
func TestBuildPeerVisualMap_NilTagLookup(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex"}, {Hostname: "annlap"},
	}
	colors, _ := buildPeerVisualMap(envoys, nil)
	if colors["annlap"] != swarmVizColors[0] {
		t.Errorf("annlap: got %q, want slot 0 color", colors["annlap"])
	}
	if colors["plex"] != swarmVizColors[1] {
		t.Errorf("plex: got %q, want slot 1 color", colors["plex"])
	}
}

// TestBuildChunkMatrix_ShapeAndCounts verifies that the matrix has one row
// and column per peer in peerColors (alphabetic), that rows are indexed by
// target (receiver) and columns by source (server), and that cell counts
// match the ChunkSources input.
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

	// Expected cells as [target][source]:
	//   plex   ← danlap: 60
	//   plex   ← annlap: 40
	//   annlap ← danlap: 100
	cases := []struct {
		target, source string
		want           int
	}{
		{"plex", "danlap", 60},
		{"plex", "annlap", 40},
		{"annlap", "danlap", 100},
		{"danlap", "annlap", 0},
		{"annlap", "plex", 0},
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
		got := m.Rows[idx(tc.target)].Cells[idx(tc.source)].Chunks
		if got != tc.want {
			t.Errorf("cell[target=%s][source=%s] = %d, want %d", tc.target, tc.source, got, tc.want)
		}
	}

	// Each row is now a target — .Target should carry the target hostname.
	for i, p := range wantPeers {
		if m.Rows[i].Target != p {
			t.Errorf("Rows[%d].Target = %q, want %q", i, m.Rows[i].Target, p)
		}
	}

	if m.MaxCount != 100 {
		t.Errorf("MaxCount = %d, want 100", m.MaxCount)
	}
	if m.Empty {
		t.Error("Empty = true, want false (matrix has transfers)")
	}
}

// TestBuildChunkMatrix_CellColorFromSource verifies that a populated cell
// is colored from the column-peer's palette entry (the source), not the
// row-peer's. This is how a sweep down any column reads as "that source's
// footprint across the fleet."
func TestBuildChunkMatrix_CellColorFromSource(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{"danlap": 50}},
	}
	peerColors := map[string]string{
		"danlap": "#f38ba8", // red — the source
		"plex":   "#a6e3a1", // green — the target
	}
	m := buildChunkMatrix(envoys, peerColors)

	// Locate the plex row (target) and danlap column (source).
	idx := func(n string) int {
		for i, p := range m.Peers {
			if p == n {
				return i
			}
		}
		t.Fatalf("peer %q not found", n)
		return -1
	}
	cell := m.Rows[idx("plex")].Cells[idx("danlap")]
	if cell.Chunks != 50 {
		t.Fatalf("chunks = %d, want 50", cell.Chunks)
	}
	// Cell bg class should derive from danlap's color (#f38ba8 → .cc-f38ba8),
	// not plex's (#a6e3a1 → .cc-a6e3a1), plus a .cc-bg-aN alpha bucket.
	if !strings.Contains(cell.BgClass, "cc-f38ba8") {
		t.Errorf("cell.BgClass = %q, want color class from source #f38ba8", cell.BgClass)
	}
	if strings.Contains(cell.BgClass, "cc-a6e3a1") {
		t.Errorf("cell.BgClass = %q, must not derive from target #a6e3a1", cell.BgClass)
	}
	if !strings.Contains(cell.BgClass, "cc-bg-a") {
		t.Errorf("cell.BgClass = %q, want a .cc-bg-aN alpha bucket", cell.BgClass)
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
		if m.Rows[i].Cells[i].BgClass != "" {
			t.Errorf("diagonal cell [%d][%d] BgClass = %q, want empty", i, i, m.Rows[i].Cells[i].BgClass)
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

// TestAlphaBucket_Bounds verifies that alphaBucket stays in [0, vizAlphaSteps]
// across the count range: zero count → bucket 0 (no cell painted), and the
// heaviest cell reaches the top bucket (full saturation). The .cc-bg-aN classes
// in style.css map each bucket to alpha 0.25 + 0.75*(N/vizAlphaSteps).
func TestAlphaBucket_Bounds(t *testing.T) {
	cases := []struct {
		count, max int
		want       int
	}{
		{0, 100, 0}, // zero count → bucket 0 (no cell painted)
		{1, 100, 0}, // lightest populated rounds to the floor bucket
		{50, 100, vizAlphaSteps / 2},
		{100, 100, vizAlphaSteps}, // full saturation at the max
	}
	for _, tc := range cases {
		got := alphaBucket(tc.count, tc.max)
		if got != tc.want {
			t.Errorf("alphaBucket(%d, %d) = %d, want %d", tc.count, tc.max, got, tc.want)
		}
		if got < 0 || got > vizAlphaSteps {
			t.Errorf("alphaBucket(%d, %d) = %d, out of [0, %d]", tc.count, tc.max, got, vizAlphaSteps)
		}
	}
}

// TestCCClass_Slug verifies ccClass maps a "#rrggbb" viz color to its
// .cc-<rrggbb> custom-property class, with a neutral fallback for malformed input.
func TestCCClass_Slug(t *testing.T) {
	if got := ccClass("#a6e3a1"); got != "cc-a6e3a1" {
		t.Errorf("ccClass(#a6e3a1) = %q, want cc-a6e3a1", got)
	}
	if got := ccClass("nonsense"); got != "cc-a0aec0" {
		t.Errorf("ccClass(bad) = %q, want neutral fallback cc-a0aec0", got)
	}
}

// TestBuildChunkMatrix_TrailerFields verifies that per-envoy status,
// progress, duration, and skip reason are threaded onto the matching
// matrix row — without these the matrix can't distinguish an off-palette
// row from a pending/skipped/cached one.
func TestBuildChunkMatrix_TrailerFields(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{
			Hostname:         "plex",
			Status:           "complete",
			ChunksHave:       100,
			ChunksNeed:       100,
			ActiveDownloadMs: 812,
			ChunkSources:     map[string]int{"danlap": 100},
		},
		{
			Hostname:     "web01",
			Status:       "skipped",
			SkipReason:   "insufficient disk space",
			ChunkSources: map[string]int{},
		},
		{
			Hostname:   "danlap",
			Status:     "source",
			ChunksHave: 100,
			ChunksNeed: 100,
		},
	}
	peerColors := map[string]string{
		"plex":   "#a6e3a1",
		"web01":  "#f9e2af",
		"danlap": "#f38ba8",
	}
	m := buildChunkMatrix(envoys, peerColors)

	rowByName := func(n string) *ChunkMatrixRow {
		for i := range m.Rows {
			if m.Rows[i].Target == n {
				return &m.Rows[i]
			}
		}
		t.Fatalf("row %q missing", n)
		return nil
	}

	plex := rowByName("plex")
	if plex.Status != "complete" || plex.ChunksHave != 100 || plex.ChunksNeed != 100 || plex.DurationMs != 812 {
		t.Errorf("plex trailer = %+v, want complete 100/100 812ms", plex)
	}
	if plex.SkipReason != "" {
		t.Errorf("plex.SkipReason = %q, want empty (status != skipped)", plex.SkipReason)
	}

	web01 := rowByName("web01")
	if web01.Status != "skipped" || web01.SkipReason != "insufficient disk space" {
		t.Errorf("web01 trailer = %+v, want skipped w/ reason", web01)
	}

	src := rowByName("danlap")
	if src.Status != "source" || src.DurationMs != 0 {
		t.Errorf("danlap trailer = %+v, want source w/ no duration", src)
	}
}

// TestBuildChunkMatrix_CachedSeparateFromCells verifies that
// ChunkSources["cached"] populates CachedChunks on the row but does NOT
// contaminate any matrix cell — cached chunks didn't flow through a peer,
// so the matrix must continue to represent only peer-to-peer transfers.
func TestBuildChunkMatrix_CachedSeparateFromCells(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{
			Hostname:     "plex",
			Status:       "complete",
			ChunkSources: map[string]int{"cached": 30, "danlap": 50},
		},
	}
	peerColors := map[string]string{
		"plex":   "#a6e3a1",
		"danlap": "#f38ba8",
	}
	m := buildChunkMatrix(envoys, peerColors)

	idx := func(n string) int {
		for i, p := range m.Peers {
			if p == n {
				return i
			}
		}
		t.Fatalf("peer %q missing", n)
		return -1
	}
	plex := m.Rows[idx("plex")]
	if plex.CachedChunks != 30 {
		t.Errorf("CachedChunks = %d, want 30", plex.CachedChunks)
	}
	if plex.Cells[idx("danlap")].Chunks != 50 {
		t.Errorf("plex←danlap cell = %d, want 50", plex.Cells[idx("danlap")].Chunks)
	}
	if plex.Total != 50 {
		t.Errorf("Total = %d, want 50 (cached excluded)", plex.Total)
	}
	// No cell should carry the cached count.
	for j, cell := range plex.Cells {
		if cell.Chunks == 30 {
			t.Errorf("cell[%d] = 30 — cached leaked into matrix cell", j)
		}
	}
}

// TestBuildChunkMatrix_DurationPctBounds verifies that DurationPct is
// computed on a single scale across the matrix: slowest row hits 100,
// faster non-zero rows stay ≥ 1, and source/no-op rows stay at 0.
func TestBuildChunkMatrix_DurationPctBounds(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "slow", Status: "complete", ActiveDownloadMs: 10000, ChunkSources: map[string]int{"seed": 10}},
		{Hostname: "fast", Status: "complete", ActiveDownloadMs: 50, ChunkSources: map[string]int{"seed": 10}},
		{Hostname: "seed", Status: "source", ActiveDownloadMs: 0},
	}
	peerColors := map[string]string{
		"slow": "#a6e3a1",
		"fast": "#f9e2af",
		"seed": "#f38ba8",
	}
	m := buildChunkMatrix(envoys, peerColors)
	rowByName := func(n string) *ChunkMatrixRow {
		for i := range m.Rows {
			if m.Rows[i].Target == n {
				return &m.Rows[i]
			}
		}
		t.Fatalf("row %q missing", n)
		return nil
	}

	if p := rowByName("slow").DurationPct; p != 100 {
		t.Errorf("slow.DurationPct = %d, want 100", p)
	}
	if p := rowByName("fast").DurationPct; p < 1 || p > 5 {
		t.Errorf("fast.DurationPct = %d, want small but >= 1 (floor clamp)", p)
	}
	if p := rowByName("seed").DurationPct; p != 0 {
		t.Errorf("seed.DurationPct = %d, want 0 (no download)", p)
	}
}

// TestBuildChunkMatrix_SourceTotalRendered verifies that SourceTotal[i]
// equals the sum of column i across rows, excluding diagonal self-cells
// and any cached pseudo-source contributions.
func TestBuildChunkMatrix_SourceTotalRendered(t *testing.T) {
	envoys := []swarm.EnvoySwarmStatus{
		{Hostname: "plex", ChunkSources: map[string]int{"danlap": 60, "annlap": 40, "cached": 99}},
		{Hostname: "annlap", ChunkSources: map[string]int{"danlap": 100}},
		{Hostname: "danlap", ChunkSources: map[string]int{}},
	}
	peerColors := map[string]string{
		"annlap": "#f9e2af",
		"danlap": "#f38ba8",
		"plex":   "#a6e3a1",
	}
	m := buildChunkMatrix(envoys, peerColors)

	idx := func(n string) int {
		for i, p := range m.Peers {
			if p == n {
				return i
			}
		}
		t.Fatalf("peer %q missing", n)
		return -1
	}
	// danlap served 60 to plex + 100 to annlap = 160
	if got := m.SourceTotal[idx("danlap")]; got != 160 {
		t.Errorf("SourceTotal[danlap] = %d, want 160", got)
	}
	// annlap served 40 to plex
	if got := m.SourceTotal[idx("annlap")]; got != 40 {
		t.Errorf("SourceTotal[annlap] = %d, want 40", got)
	}
	// plex served nothing
	if got := m.SourceTotal[idx("plex")]; got != 0 {
		t.Errorf("SourceTotal[plex] = %d, want 0", got)
	}
}
