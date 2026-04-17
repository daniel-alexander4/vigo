// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// Maximum columns in the chunk heatmap. Larger files are bucketed.
const maxHeatmapColumns = 200

// Maximum envoys shown in the swarm visualization. Matches the size of
// the three-tier color palette: 8 Mocha accents, then those same 8 in
// Latte (darker) variants, then the 8 Mocha accents again with a thin
// Crust border. Fleets larger than 24 drop the excess envoys entirely
// from the matrix / heatmap / duration bars — legend saturation is a
// harder failure than hiding envoys, per research cited in features.md.
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

// SwarmVizPageData extends SwarmPageData with visualization data.
//
// Peer identity is a deterministic (color, tier) pair assigned by
// buildPeerVisualMap based on alphabetic envoy order. Tiers 0/1/2
// correspond to slots [0-7] / [8-15] / [16-23] in swarmVizColors.
type SwarmVizPageData struct {
	SwarmPageData
	HeatmapRows    []HeatmapRow
	HeatmapColumns int
	Matrix         *ChunkMatrix
	DurationBars   []DurationBar
	PeerColors     map[string]string // hostname → hex color from swarmVizColors
	PeerTiers      map[string]int    // hostname → 0, 1, or 2
	VizCapped      bool              // true when visualizations show a subset of envoys
	VizTotal       int               // total envoy count when capped
}

// HeatmapRow is one envoy's row in the chunk distribution heatmap.
type HeatmapRow struct {
	Hostname string
	Status   string // overall status: source, complete, downloading, pending, skipped
	Buckets  []ChunkBucket
}

// ChunkBucket is an aggregated group of chunks for heatmap rendering.
type ChunkBucket struct {
	Status string // "complete", "downloading", "missing"
	Source string // dominant source peer
	Start  int
	End    int
}

// ChunkMatrixCell is one cell in the source × target chunk-flow matrix.
// BgColor is a pre-computed rgba(...) string with alpha proportional to
// Chunks relative to MaxCount. Empty for zero-count and diagonal cells.
type ChunkMatrixCell struct {
	Chunks  int
	BgColor string
}

// ChunkMatrixRow is one source-peer row in the chunk-flow matrix.
type ChunkMatrixRow struct {
	Source string
	Color  string // source-peer label color
	Cells  []ChunkMatrixCell
	Total  int // total chunks this source served
}

// ChunkMatrix is the source × target chunk-flow matrix. Rows and columns
// share the same ordered peer list (alphabetic by hostname); cell
// [row][col] carries the chunk count transferred from row-peer to
// col-peer. The diagonal is always empty by construction.
type ChunkMatrix struct {
	Peers    []string
	Colors   map[string]string
	Rows     []ChunkMatrixRow
	ColTotal []int // total chunks each target received
	MaxCount int
	Empty    bool // true when no peer-to-peer transfers occurred
}

// DurationBar is one envoy's total download duration, rendered as a
// horizontal bar. PercentOfMax is the bar width as a percentage of the
// slowest envoy's duration so the viewer can compare at a glance.
type DurationBar struct {
	Hostname     string
	Color        string
	DurationMs   int64
	PercentOfMax int
}

// SwarmViz renders the viz list or detail page.
func (h *Handler) SwarmViz(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	if sha != "" {
		h.swarmVizDetail(w, r, sha)
		return
	}
	h.swarmVizList(w, r)
}

func (h *Handler) swarmVizList(w http.ResponseWriter, r *http.Request) {
	data := SwarmVizPageData{}
	data.PageData = PageData{Title: "Swarm Visualizations", ActiveNav: "swarm"}

	var totalSize int64

	if h.swarmTracker != nil {
		for sha, seed := range h.swarmTracker.ListSeeds() {
			size := int64(seed.Size)
			totalSize += size
			chunks := chunksForSize(size)
			entry := SwarmBlobEntry{
				SHA256:    sha,
				Label:     seed.Label,
				Size:      size,
				SizeHuman: humanSize(size),
				Chunks:    chunks,
				CreatedAt: seed.DistributedAt,
				Source:    h.seedSourceName(seed.SeederID),
			}
			statuses := h.swarmTracker.Status(sha, chunks, seed.SeederID)
			entry.Summary = summarizeSwarmStatuses(statuses)
			if entry.Summary.Downloading > 0 || entry.Summary.Pending > 0 {
				data.ActiveDistributions++
				entry.Status = "distributing"
			} else if entry.Summary.Complete == 0 && entry.Summary.Cached > 0 {
				entry.Status = "cached"
			} else {
				entry.Status = "complete"
			}
			data.Blobs = append(data.Blobs, entry)
		}

		for sha, seed := range h.swarmTracker.ListRevokedSeeds() {
			size := int64(seed.Size)
			chunks := chunksForSize(size)
			entry := SwarmBlobEntry{
				SHA256:    sha,
				Label:     seed.Label,
				Size:      size,
				SizeHuman: humanSize(size),
				Chunks:    chunks,
				CreatedAt: seed.DistributedAt,
				Source:    h.seedSourceName(seed.SeederID),
				Revoked:   true,
				Status:    "revoked",
			}
			statuses := h.swarmTracker.Status(sha, chunks, seed.SeederID)
			entry.Summary = summarizeSwarmStatuses(statuses)
			data.Blobs = append(data.Blobs, entry)
		}
	}

	data.TotalBlobs = len(data.Blobs)
	data.TotalSizeHuman = humanSize(totalSize)
	if data.TotalSizeHuman == "" {
		data.TotalSizeHuman = "0 B"
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "swarm-viz", data)
}

func (h *Handler) swarmVizDetail(w http.ResponseWriter, r *http.Request, sha string) {
	data := SwarmVizPageData{}
	data.PageData = PageData{Title: "Swarm Visualizations", ActiveNav: "swarm"}

	var detail *SwarmBlobDetail
	if h.swarmTracker != nil {
		if seed, ok := h.swarmTracker.GetSeed(sha); ok {
			size := int64(seed.Size)
			chunks := chunksForSize(size)
			detail = &SwarmBlobDetail{
				SHA256:    sha,
				Label:     seed.Label,
				Size:      size,
				SizeHuman: humanSize(size),
				Chunks:    chunks,
				CreatedAt: seed.DistributedAt,
				Source:    h.seedSourceName(seed.SeederID),
				Retention: formatRetention(seed.Retention),
			}
		}
	}
	if detail == nil {
		http.NotFound(w, r)
		return
	}

	if h.swarmTracker != nil {
		seederID := ""
		if seed, ok := h.swarmTracker.GetSeed(sha); ok {
			seederID = seed.SeederID
		}
		statuses := h.swarmTracker.Status(sha, detail.Chunks, seederID)
		resolveSwarmHostnames(statuses, h.swarmFleetIndex)
		sortSwarmStatuses(statuses)
		detail.Summary = summarizeSwarmStatuses(statuses)
		detail.Envoys = statuses
	}

	data.BlobDetail = detail

	// Cap visualizations to 24 envoys — the size of our three-tier color
	// palette. Excess envoys are dropped entirely from the viz (not just
	// hidden behind a "+N more" row) because legend saturation is a
	// harder failure than omission. The text table above still shows all
	// envoys; the cap only affects the Sankey / heatmap / duration bars.
	vizEnvoys := topVizEnvoys(detail.Envoys, maxVizEnvoys)
	if len(detail.Envoys) > maxVizEnvoys {
		data.VizCapped = true
		data.VizTotal = len(detail.Envoys)
	}

	data.PeerColors, data.PeerTiers = buildPeerVisualMap(vizEnvoys)
	data.HeatmapColumns = min(detail.Chunks, maxHeatmapColumns)
	data.HeatmapRows = buildHeatmapRows(vizEnvoys, detail.Chunks, data.HeatmapColumns)
	data.Matrix = buildChunkMatrix(vizEnvoys, data.PeerColors)
	data.DurationBars = buildDurationBars(vizEnvoys, data.PeerColors)

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "swarm-viz", data)
}

// SwarmVizEnvoysFragment serves the auto-refresh fragment for the viz detail.
func (h *Handler) SwarmVizEnvoysFragment(w http.ResponseWriter, r *http.Request) {
	sha := r.URL.Query().Get("sha256")
	if sha == "" || h.swarmTracker == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var size int64
	if seed, ok := h.swarmTracker.GetSeed(sha); ok {
		size = int64(seed.Size)
	}
	if size == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	chunks := chunksForSize(size)
	seederID := ""
	if seed, ok := h.swarmTracker.GetSeed(sha); ok {
		seederID = seed.SeederID
	}
	statuses := h.swarmTracker.Status(sha, chunks, seederID)
	resolveSwarmHostnames(statuses, h.swarmFleetIndex)
	sortSwarmStatuses(statuses)

	// Cap visualizations to 24 envoys — see swarmVizDetail for the
	// reasoning. Excess envoys are dropped entirely from the viz.
	vizEnvoys := topVizEnvoys(statuses, maxVizEnvoys)
	vizCapped := len(statuses) > maxVizEnvoys
	vizTotal := len(statuses)

	cols := min(chunks, maxHeatmapColumns)
	peerColors, peerTiers := buildPeerVisualMap(vizEnvoys)

	vizData := &SwarmVizPageData{
		HeatmapRows:    buildHeatmapRows(vizEnvoys, chunks, cols),
		HeatmapColumns: cols,
		Matrix:         buildChunkMatrix(vizEnvoys, peerColors),
		DurationBars:   buildDurationBars(vizEnvoys, peerColors),
		PeerColors:     peerColors,
		PeerTiers:      peerTiers,
		VizCapped:      vizCapped,
		VizTotal:       vizTotal,
	}
	vizData.BlobDetail = &SwarmBlobDetail{
		SHA256:  sha,
		Chunks:  chunks,
		Summary: summarizeSwarmStatuses(statuses),
		Envoys:  statuses,
	}

	h.renderer.RenderFragment(w, "swarm-viz", "swarm-viz-envoys-fragment", vizData)
}

// buildHeatmapRows aggregates per-envoy chunk data into bucketed rows.
func buildHeatmapRows(envoys []swarm.EnvoySwarmStatus, totalChunks, maxBuckets int) []HeatmapRow {
	if maxBuckets <= 0 {
		maxBuckets = 1
	}
	bucketSize := (totalChunks + maxBuckets - 1) / maxBuckets

	rows := make([]HeatmapRow, len(envoys))
	for i, envoy := range envoys {
		rows[i] = HeatmapRow{
			Hostname: envoy.Hostname,
			Status:   envoy.Status,
			Buckets:  bucketChunksForEnvoy(envoy, totalChunks, maxBuckets, bucketSize),
		}
	}
	return rows
}

func bucketChunksForEnvoy(envoy swarm.EnvoySwarmStatus, totalChunks, maxBuckets, bucketSize int) []ChunkBucket {
	// Build chunk presence map from per-chunk detail (the authoritative source).
	received := make(map[int]string, len(envoy.ChunkDetails))
	for _, d := range envoy.ChunkDetails {
		received[d.Chunk] = d.Source
	}

	// When per-chunk detail is unavailable but aggregate source counts exist
	// (e.g., cached/resurrected blobs whose .sources file was deleted during
	// eviction, or PeerTracker data that survived a revoke→reseed cycle),
	// synthesize a proportional source sequence so the strip chart shows
	// colored bars instead of all-grey. The ordering is approximate — we
	// distribute sources round-robin weighted by their chunk counts — but
	// it's far more useful than showing no attribution at all.
	if len(received) == 0 && len(envoy.ChunkSources) > 0 {
		received = synthesizeSourceMap(envoy.ChunkSources, totalChunks)
	}

	buckets := make([]ChunkBucket, maxBuckets)
	for b := 0; b < maxBuckets; b++ {
		start := b * bucketSize
		end := start + bucketSize - 1
		if end >= totalChunks {
			end = totalChunks - 1
		}

		have, total := 0, 0
		sourceCounts := map[string]int{}
		for c := start; c <= end; c++ {
			total++
			if src, ok := received[c]; ok {
				have++
				sourceCounts[src]++
			}
		}

		status := "missing"
		// The seeder has every chunk by construction — render its
		// heatmap row as fully complete without relying on per-chunk
		// receive records (which the seeder never produces).
		if envoy.Status == "source" || envoy.Status == "complete" || envoy.Status == "cached" || (total > 0 && have == total) {
			status = "complete"
		} else if have > 0 {
			status = "downloading"
		}
		if envoy.Status == "pending" {
			status = "missing"
		}
		if envoy.Status == "skipped" {
			status = "skipped"
		}

		buckets[b] = ChunkBucket{
			Status: status,
			Source: dominantSource(sourceCounts),
			Start:  start,
			End:    end,
		}
	}
	return buckets
}

// synthesizeSourceMap builds a chunk→source map from aggregate ChunkSources
// when per-chunk ChunkDetails is unavailable. Sources are distributed
// proportionally: if danlap served 75% of chunks and girlslaptop 25%, the
// first 75% of synthetic chunk indices are attributed to danlap and the
// remainder to girlslaptop. This produces clean contiguous color bands in
// the strip chart rather than a noisy interleave.
func synthesizeSourceMap(sources map[string]int, totalChunks int) map[int]string {
	if len(sources) == 0 || totalChunks <= 0 {
		return nil
	}

	// Sort sources by count descending for deterministic, visually stable
	// rendering across refreshes. Ties broken alphabetically.
	type srcCount struct {
		source string
		count  int
	}
	sorted := make([]srcCount, 0, len(sources))
	for src, count := range sources {
		if src == "cached" {
			continue // don't attribute to "cached" pseudo-source
		}
		sorted = append(sorted, srcCount{src, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].count != sorted[j].count {
			return sorted[i].count > sorted[j].count
		}
		return sorted[i].source < sorted[j].source
	})

	result := make(map[int]string, totalChunks)
	idx := 0
	for _, sc := range sorted {
		for c := 0; c < sc.count && idx < totalChunks; c++ {
			result[idx] = sc.source
			idx++
		}
	}
	return result
}

func dominantSource(counts map[string]int) string {
	best, bestCount := "", 0
	for src, count := range counts {
		if count > bestCount {
			best = src
			bestCount = count
		}
	}
	return best
}

// buildChunkMatrix produces the source × target chunk-flow matrix. The
// peer axis is the alphabetically-sorted set of hostnames in peerColors
// (which is itself already capped to the 24-slot palette by
// buildPeerVisualMap). A peer that appears only as a ChunkSources key —
// for example a cross-subnet seeder outside the capped envoy list — is
// dropped, matching the Sankey's prior behavior: if it's not in the
// legend, it doesn't show up in the matrix.
//
// Cells are pre-colored server-side so the template stays presentational.
// BgColor is rgba(r,g,b,alpha) with alpha scaled between 0.25 and 1.0
// based on Chunks/MaxCount so zero-weight cells render as inert and the
// heaviest transfer is fully saturated.
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
			counts[sIdx][tIdx] += count
			if counts[sIdx][tIdx] > maxCount {
				maxCount = counts[sIdx][tIdx]
			}
		}
	}

	rows := make([]ChunkMatrixRow, n)
	colTotal := make([]int, n)
	for i, src := range peers {
		cells := make([]ChunkMatrixCell, n)
		rowTotal := 0
		for j := 0; j < n; j++ {
			c := counts[i][j]
			cells[j] = ChunkMatrixCell{Chunks: c}
			if c > 0 && i != j {
				cells[j].BgColor = rgbaFromHex(peerColors[src], cellAlpha(c, maxCount))
				rowTotal += c
				colTotal[j] += c
			}
		}
		rows[i] = ChunkMatrixRow{
			Source: src,
			Color:  peerColors[src],
			Cells:  cells,
			Total:  rowTotal,
		}
	}

	return &ChunkMatrix{
		Peers:    peers,
		Colors:   peerColors,
		Rows:     rows,
		ColTotal: colTotal,
		MaxCount: maxCount,
		Empty:    maxCount == 0,
	}
}

// cellAlpha maps a chunk count to an opacity in [0.25, 1.0] so the
// lightest populated cell still reads as "non-empty" and the heaviest
// saturates to the full peer color.
func cellAlpha(count, max int) float64 {
	if max <= 0 || count <= 0 {
		return 0
	}
	return 0.25 + 0.75*float64(count)/float64(max)
}

// rgbaFromHex parses a "#rrggbb" color and returns a CSS rgba() string
// with the given alpha. Returns the hex unchanged if it isn't in the
// expected form — swarmVizColors is authoritative here, so the fallback
// only runs if the palette is ever changed to use non-hex values.
func rgbaFromHex(hex string, alpha float64) string {
	if len(hex) != 7 || hex[0] != '#' {
		return hex
	}
	r, err1 := strconv.ParseUint(hex[1:3], 16, 8)
	g, err2 := strconv.ParseUint(hex[3:5], 16, 8)
	b, err3 := strconv.ParseUint(hex[5:7], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return hex
	}
	return fmt.Sprintf("rgba(%d,%d,%d,%.3f)", r, g, b, alpha)
}

// buildDurationBars produces one row per envoy that reported a non-zero
// ActiveDownloadMs. Rows are sorted slowest → fastest so the slowest
// envoy's bar fills the track and the viewer can compare the rest against
// it. PercentOfMax is computed against the slowest so the fastest envoy
// never collapses to an invisible sliver — bars are always at least 1%.
func buildDurationBars(envoys []swarm.EnvoySwarmStatus, peerColors map[string]string) []DurationBar {
	var bars []DurationBar
	for _, envoy := range envoys {
		if envoy.ActiveDownloadMs <= 0 {
			continue
		}
		color := peerColors[envoy.Hostname]
		if color == "" {
			color = "#a0aec0"
		}
		bars = append(bars, DurationBar{
			Hostname:   envoy.Hostname,
			Color:      color,
			DurationMs: envoy.ActiveDownloadMs,
		})
	}
	if len(bars) == 0 {
		return nil
	}

	sort.Slice(bars, func(i, j int) bool { return bars[i].DurationMs > bars[j].DurationMs })

	maxMs := bars[0].DurationMs
	if maxMs <= 0 {
		maxMs = 1
	}
	for i := range bars {
		pct := int((bars[i].DurationMs * 100) / maxMs)
		if pct < 1 {
			pct = 1
		}
		bars[i].PercentOfMax = pct
	}
	return bars
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
func buildPeerVisualMap(envoys []swarm.EnvoySwarmStatus) (colors map[string]string, tiers map[string]int) {
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

