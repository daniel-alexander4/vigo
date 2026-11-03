// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"sort"
	"strconv"
	"strings"

	"git.alexander4.io/vigo/server/swarm"
)

// Maximum envoys shown in the swarm visualization. Matches the size of
// the three-tier color palette: 8 Mocha accents, then those same 8 in
// Latte (darker) variants, then the 8 Mocha accents again. Fleets larger
// than 24 drop the excess envoys entirely from the matrix — legend
// saturation is a harder failure than hiding envoys, per research cited
// in features.md.
const maxVizEnvoys = 24

// swarmVizColors — 24-slot palette used by buildPeerVisualMap for swarm
// visualizations only. Ordering is load-bearing: small fleets (≤8 envoys)
// land exclusively in Tier 1, medium fleets (≤16) spill into Tier 2, and
// only fleets above 16 reach Tier 3. This gives the smallest fleets the
// cleanest color identification.
//
//   - Slots  0-7: Tier 1 — Catppuccin Mocha. Light pastels.
//   - Slots  8-15: Tier 2 — Catppuccin Latte. Deeper/saturated variants
//     of the same 8 hues. Designed as a coherent Catppuccin palette
//     variant, not ad-hoc HSL darkening.
//   - Slots 16-23: Tier 3 — Catppuccin Mocha again. The matrix cell
//     background carries intensity information, so Tier 3 is marked
//     only via the PeerTiers map for any future styling hook.
var swarmVizColors = []string{
	// Tier 1 — Mocha
	"#f9e2af", // Yellow
	"#f38ba8", // Red
	"#a6e3a1", // Green
	"#89b4fa", // Blue
	"#fab387", // Peach
	"#cba6f7", // Mauve
	"#94e2d5", // Teal
	"#f5c2e7", // Pink
	// Tier 2 — Latte (darker variants of the same 8 hues)
	"#df8e1d", // Yellow
	"#d20f39", // Red
	"#40a02b", // Green
	"#1e66f5", // Blue
	"#fe640b", // Peach
	"#8839ef", // Mauve
	"#179299", // Teal
	"#ea76cb", // Pink
	// Tier 3 — Mocha again (same hues as Tier 1)
	"#f9e2af", // Yellow
	"#f38ba8", // Red
	"#a6e3a1", // Green
	"#89b4fa", // Blue
	"#fab387", // Peach
	"#cba6f7", // Mauve
	"#94e2d5", // Teal
	"#f5c2e7", // Pink
}

// ChunkMatrixCell is one cell in the target × source chunk-flow matrix.
// BgClass is the CSP-safe class pair "cc-<rrggbb> cc-bg-aN" derived from the
// column-peer (the source): cc-<rrggbb> sets --cc-rgb, cc-bg-aN paints it at
// an alpha bucket proportional to Chunks/MaxCount. Empty for zero-count and
// diagonal cells.
type ChunkMatrixCell struct {
	Chunks  int
	BgClass string
}

// ChunkMatrixRow is one target-peer row in the chunk-flow matrix. The
// row represents the envoy that received chunks; each cell is the count
// of chunks received from the corresponding column-peer (source). The
// trailer fields carry per-envoy status that the matrix cells don't —
// cached chunks don't flow through a peer, skipped envoys produce no
// cells at all, and "pending" reads identical to "off-palette" without
// a status pill to disambiguate.
type ChunkMatrixRow struct {
	Target     string
	ColorClass string // .cc-<rrggbb> target-peer label color class (sets --cc-rgb)
	Cells      []ChunkMatrixCell
	Total      int // chunks this target received from peers (cells sum)

	// Trailer fields — rendered to the right of the matrix cells.
	Status       string // source / complete / cached / downloading / pending / skipped
	ChunksHave   int
	ChunksNeed   int
	CachedChunks int    // ChunkSources["cached"] — not represented in cells
	DurationMs   int64  // wall-clock download time (0 for source/no-op rows)
	DurationPct  int    // 1..100 relative to slowest row; 0 when DurationMs == 0
	SkipReason   string // only populated when Status == "skipped"
}

// ChunkMatrix is the target × source chunk-flow matrix. Rows are
// receiving envoys, columns are the peers that served them; both axes
// share the same ordered peer list (alphabetic by hostname). Cell
// [row][col] carries the chunk count that target-peer[row] received
// from source-peer[col]. The diagonal is always empty by construction.
type ChunkMatrix struct {
	Peers       []string
	Colors      map[string]string
	Rows        []ChunkMatrixRow
	SourceTotal []int // total chunks each source served (indexed by Peers)
	MaxCount    int
	Empty       bool // true when no peer-to-peer transfers occurred
}

// buildChunkMatrix produces the target × source chunk-flow matrix.
// Rows are receivers (targets); columns are the peers that served them
// (sources). The peer axis is the alphabetically-sorted set of hostnames
// in peerColors (which is itself already capped to the 24-slot palette
// by buildPeerVisualMap). A peer that appears only as a ChunkSources key
// — for example a cross-subnet seeder outside the capped envoy list —
// is dropped: if it's not in the legend, it doesn't show up in the
// matrix.
//
// Cell backgrounds are pre-colored by the source (column-peer) so a
// sweep down any column reads as "this source's footprint across
// receivers." Alpha is scaled 0.25..1.0 by Chunks/MaxCount so
// low-weight cells stay readable and the heaviest transfer saturates.
func buildChunkMatrix(envoys []swarm.EnvoySwarmStatus, peerColors map[string]string) *ChunkMatrix {
	peers := make([]string, 0, len(peerColors))
	for p := range peerColors {
		peers = append(peers, p)
	}
	sort.Strings(peers)

	idx := make(map[string]int, len(peers))
	for i, p := range peers {
		idx[p] = i
	}

	n := len(peers)
	// counts[target][source] — indexed target-first so rows are receivers.
	counts := make([][]int, n)
	for i := range counts {
		counts[i] = make([]int, n)
	}

	maxCount := 0
	for _, envoy := range envoys {
		tIdx, ok := idx[envoy.Hostname]
		if !ok {
			continue
		}
		for src, count := range envoy.ChunkSources {
			if src == envoy.Hostname || src == "cached" {
				continue
			}
			sIdx, ok := idx[src]
			if !ok {
				continue
			}
			counts[tIdx][sIdx] += count
			if counts[tIdx][sIdx] > maxCount {
				maxCount = counts[tIdx][sIdx]
			}
		}
	}

	// Index envoys by hostname for O(1) trailer-field lookup and find the
	// slowest duration across the matrix so DurationPct renders on a
	// single scale. "cached" is picked up separately from ChunkSources
	// since matrix cells deliberately exclude it.
	envoyByHost := make(map[string]*swarm.EnvoySwarmStatus, len(envoys))
	var maxDuration int64
	for i := range envoys {
		envoyByHost[envoys[i].Hostname] = &envoys[i]
		if envoys[i].ActiveDownloadMs > maxDuration {
			maxDuration = envoys[i].ActiveDownloadMs
		}
	}

	rows := make([]ChunkMatrixRow, n)
	sourceTotal := make([]int, n)
	for i, target := range peers {
		cells := make([]ChunkMatrixCell, n)
		rowTotal := 0
		for j := 0; j < n; j++ {
			c := counts[i][j]
			cells[j] = ChunkMatrixCell{Chunks: c}
			if c > 0 && i != j {
				cells[j].BgClass = ccClass(peerColors[peers[j]]) + " cc-bg-a" + strconv.Itoa(alphaBucket(c, maxCount))
				rowTotal += c
				sourceTotal[j] += c
			}
		}

		row := ChunkMatrixRow{
			Target:     target,
			ColorClass: ccClass(peerColors[target]),
			Cells:      cells,
			Total:      rowTotal,
		}
		if e, ok := envoyByHost[target]; ok {
			row.Status = e.Status
			row.ChunksHave = e.ChunksHave
			row.ChunksNeed = e.ChunksNeed
			row.DurationMs = e.ActiveDownloadMs
			if row.Status == "skipped" {
				row.SkipReason = e.SkipReason
			}
			if n, ok := e.ChunkSources["cached"]; ok {
				row.CachedChunks = n
			}
			if maxDuration > 0 && e.ActiveDownloadMs > 0 {
				pct := int((e.ActiveDownloadMs * 100) / maxDuration)
				if pct < 1 {
					pct = 1
				}
				row.DurationPct = pct
			}
		}
		rows[i] = row
	}

	return &ChunkMatrix{
		Peers:       peers,
		Colors:      peerColors,
		Rows:        rows,
		SourceTotal: sourceTotal,
		MaxCount:    maxCount,
		Empty:       maxCount == 0,
	}
}

// vizAlphaSteps is the number of opacity buckets the continuous cell alpha
// (0.25..1.0 by Chunks/MaxCount) is quantized into for the .cc-bg-aN classes.
// The classes in style.css are generated at alpha = 0.25 + 0.75*(N/vizAlphaSteps).
const vizAlphaSteps = 16

// alphaBucket maps a chunk count to an opacity bucket 0..vizAlphaSteps so the
// lightest populated cell still reads as "non-empty" (bucket carries the same
// 0.25 floor in CSS) and the heaviest saturates to the full peer color.
func alphaBucket(count, max int) int {
	if max <= 0 || count <= 0 {
		return 0
	}
	b := int(float64(count)/float64(max)*float64(vizAlphaSteps) + 0.5)
	if b > vizAlphaSteps {
		b = vizAlphaSteps
	}
	return b
}

// vizNamedColors maps human-readable color names to the eight base
// hues in the swarm viz palette (Mocha tier). An operator who tags
// `plex` with `viz.color=green` can pin plex to that hue across
// redeploys; the tag value is matched case-insensitively.
var vizNamedColors = map[string]string{
	"yellow": "#f9e2af",
	"red":    "#f38ba8",
	"green":  "#a6e3a1",
	"blue":   "#89b4fa",
	"peach":  "#fab387",
	"mauve":  "#cba6f7",
	"teal":   "#94e2d5",
	"pink":   "#f5c2e7",
}

// vizColorTagOverride scans `tags` for a `viz.color=<name>` entry and
// returns the matching hex code if the value resolves to a known
// named color or already looks like a hex code (#rrggbb / #rgb).
// Empty string when no override applies. Other `viz.*` tags
// (`viz.shape`, `viz.dash`) are accepted by the parser but ignored
// today — the swarm viz expresses variation via tier (palette slot
// 0-7 vs 8-15 vs 16-23), not separate shape/dash channels.
func vizColorTagOverride(tags []string) string {
	for _, t := range tags {
		eq := strings.IndexByte(t, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(t[:eq])
		if !strings.EqualFold(key, "viz.color") {
			continue
		}
		val := strings.TrimSpace(t[eq+1:])
		if val == "" {
			continue
		}
		if hex, ok := vizNamedColors[strings.ToLower(val)]; ok {
			return hex
		}
		// Pass-through hex codes (`#rrggbb` or `#rgb`) — operators who
		// want a custom hue don't need to wait for it to land in the
		// named palette. Validate length + leading `#`; reject
		// everything else so a typo doesn't silently apply garbage.
		if (len(val) == 7 || len(val) == 4) && val[0] == '#' {
			return val
		}
	}
	return ""
}

// buildPeerVisualMap assigns a (color, tier) pair to each envoy deterministically
// by alphabetic order. Slot N → swarmVizColors[N]; tier = N/8 (0, 1, or 2).
//
// Small fleets stay entirely in Tier 1 — envoy #0 alphabetically gets slot 0
// (bright Mocha Yellow), #1 gets Red, and so on. Only when the fleet exceeds
// 8 does anyone spill into Tier 2 (Latte); only above 16 does anyone reach
// Tier 3 (Mocha with border). Envoys beyond slot 23 are dropped from the
// returned maps entirely and also dropped from the viz (see the maxVizEnvoys
// cap in the callers).
//
// Peers appearing only as chunk sources (e.g. a seeder outside the capped
// envoy list) don't get slots either — their ribbons are dropped by
// buildTopologyJSON's lookup rather than being rendered in a fallback color.
// This keeps the legend honest: if it's in the viz, it has a slot.
//
// Operator pinning: if `tagLookup` is non-nil and an envoy has a
// `viz.color=<name|hex>` tag, that hue replaces the auto-assigned
// slot color. The tier is left at the slot's natural value so the
// legend keeps its (color, tier) pairing intact for the host.
// `tagLookup` may be nil; in that case the function falls back to
// the previous hash-only behavior (test path).
func buildPeerVisualMap(envoys []swarm.EnvoySwarmStatus, tagLookup func(string) []string) (colors map[string]string, tiers map[string]int) {
	colors = make(map[string]string, len(envoys))
	tiers = make(map[string]int, len(envoys))

	// Dedup and sort envoy hostnames alphabetically for stable slot assignment.
	seen := make(map[string]struct{}, len(envoys))
	hostnames := make([]string, 0, len(envoys))
	for _, envoy := range envoys {
		if _, ok := seen[envoy.Hostname]; ok {
			continue
		}
		seen[envoy.Hostname] = struct{}{}
		hostnames = append(hostnames, envoy.Hostname)
	}
	sort.Strings(hostnames)

	for i, name := range hostnames {
		if i >= len(swarmVizColors) {
			break
		}
		colors[name] = swarmVizColors[i]
		tiers[name] = i / 8
		// Operator override via `viz.color=<name|hex>` tag.
		if tagLookup != nil {
			if hex := vizColorTagOverride(tagLookup(name)); hex != "" {
				colors[name] = hex
			}
		}
	}
	return colors, tiers
}

// topVizEnvoys picks the first N envoys alphabetically by hostname. Stable
// order across refreshes is critical: it means every envoy keeps the same
// color slot regardless of whether its status is downloading or complete
// at the moment the viz renders. Previously this sorted by status first
// (downloading→pending→complete) which meant an envoy's color *changed*
// mid-distribution as it transitioned states.
func topVizEnvoys(envoys []swarm.EnvoySwarmStatus, n int) []swarm.EnvoySwarmStatus {
	cp := make([]swarm.EnvoySwarmStatus, len(envoys))
	copy(cp, envoys)

	sort.SliceStable(cp, func(i, j int) bool {
		return cp[i].Hostname < cp[j].Hostname
	})
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}
