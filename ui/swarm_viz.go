// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// Maximum columns in the chunk heatmap. Larger files are bucketed.
const maxHeatmapColumns = 200

// Maximum envoys shown in the swarm visualization. Matches the size of
// the three-tier color palette: 8 Mocha accents, then those same 8 in
// Latte (darker) variants, then the 8 Mocha accents again with a thin
// Crust border. Fleets larger than 24 drop the excess envoys entirely
// from the Sankey / heatmap / duration bars — legend saturation is a
// harder failure than hiding envoys, per research cited in features.md.
const maxVizEnvoys = 24

// swarmVizColors — 24-slot palette used by buildPeerVisualMap for swarm
// visualizations only. Ordering is load-bearing: small fleets (≤8 envoys)
// land exclusively in Tier 1, medium fleets (≤16) spill into Tier 2, and
// only fleets above 16 reach the bordered Tier 3. This gives the smallest
// fleets the cleanest color identification.
//
//   - Slots  0-7: Tier 1 — Catppuccin Mocha. Light pastels, no border.
//   - Slots  8-15: Tier 2 — Catppuccin Latte. Deeper/saturated variants
//     of the same 8 hues. Designed as a coherent Catppuccin palette
//     variant, not ad-hoc HSL darkening.
//   - Slots 16-23: Tier 3 — Catppuccin Mocha again, JS side applies a
//     Crust (#11111b) border on the node rect and double-strokes the
//     ribbons so Tier 3 envoys are visually distinct from Tier 1 despite
//     sharing hex values.
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
	// Tier 3 — Mocha again, JS adds border + ribbon outline
	"#f9e2af", // Yellow
	"#f38ba8", // Red
	"#a6e3a1", // Green
	"#89b4fa", // Blue
	"#fab387", // Peach
	"#cba6f7", // Mauve
	"#94e2d5", // Teal
	"#f5c2e7", // Pink
}

// swarmVizBorderColor is the stroke applied to Tier 3 node rectangles and
// layered under Tier 3 ribbons in the Sankey. Catppuccin Crust — near-black
// but stays on-theme against the Mocha UI background.
const swarmVizBorderColor = "#11111b"

// SwarmVizPageData extends SwarmPageData with visualization data.
//
// Peer identity is a deterministic (color, tier) pair assigned by
// buildPeerVisualMap based on alphabetic envoy order. Tiers 0/1/2
// correspond to slots [0-7] / [8-15] / [16-23] in swarmVizColors; tier 2
// additionally gets a Crust border on its Sankey node and a black outline
// on its ribbons.
type SwarmVizPageData struct {
	SwarmPageData
	HeatmapRows    []HeatmapRow
	HeatmapColumns int
	TopologyJSON   template.JS
	DurationBars   []DurationBar
	PeerColors     map[string]string // hostname → hex color from swarmVizColors
	PeerTiers      map[string]int    // hostname → 0, 1, or 2 (for border/outline styling)
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

// TopologyNode is an envoy in the peer topology graph.
//
// Color is the hostname's assigned Catppuccin hex from swarmVizColors.
// Tier (0, 1, or 2) tells the JS renderer whether the node gets a Crust
// border and whether its outgoing ribbons get a black outline — Tier 2
// envoys share hex values with Tier 0, so the tier field is the only way
// to distinguish them visually.
type TopologyNode struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Chunks int    `json:"chunks"`
	Color  string `json:"color"`
	Tier   int    `json:"tier"`
}

// TopologyEdge is a chunk transfer between peers.
type TopologyEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Chunks int    `json:"chunks"`
}

// TopologyData is the full graph passed to D3.
type TopologyData struct {
	Nodes []TopologyNode `json:"nodes"`
	Edges []TopologyEdge `json:"edges"`
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
	data.TopologyJSON = buildTopologyJSON(vizEnvoys, data.PeerColors, data.PeerTiers)
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
		TopologyJSON:   buildTopologyJSON(vizEnvoys, peerColors, peerTiers),
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
	// Build chunk presence map.
	received := make(map[int]string, len(envoy.ChunkDetails))
	for _, d := range envoy.ChunkDetails {
		received[d.Chunk] = d.Source
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
		if envoy.Status == "source" || envoy.Status == "complete" || (total > 0 && have == total) {
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

// buildTopologyJSON creates the Sankey graph data. peerColors and peerTiers
// are the hostname→visual maps built by buildPeerVisualMap. Nodes whose
// hostname isn't in the peer maps (e.g. a chunk source that's a cross-subnet
// seeder we didn't fit in the 24-slot palette) are dropped — without a
// color assignment they'd render as fallback-gray and clutter the viz.
func buildTopologyJSON(envoys []swarm.EnvoySwarmStatus, peerColors map[string]string, peerTiers map[string]int) template.JS {
	nodeMap := make(map[string]*TopologyNode)
	var edges []TopologyEdge

	for _, envoy := range envoys {
		color, ok := peerColors[envoy.Hostname]
		if !ok {
			continue // not in the palette — dropped from viz
		}
		if _, exists := nodeMap[envoy.Hostname]; !exists {
			nodeMap[envoy.Hostname] = &TopologyNode{
				ID:     envoy.Hostname,
				Status: envoy.Status,
				Chunks: envoy.ChunksHave,
				Color:  color,
				Tier:   peerTiers[envoy.Hostname],
			}
		}

		for src, count := range envoy.ChunkSources {
			if src == envoy.Hostname || src == "cached" {
				continue
			}
			srcColor, ok := peerColors[src]
			if !ok {
				continue // source not in the palette — drop the edge
			}
			// Ensure source node exists.
			if _, exists := nodeMap[src]; !exists {
				nodeMap[src] = &TopologyNode{
					ID:     src,
					Status: "complete", // if it's serving chunks, it has them
					Color:  srcColor,
					Tier:   peerTiers[src],
				}
			}
			edges = append(edges, TopologyEdge{
				Source: src,
				Target: envoy.Hostname,
				Chunks: count,
			})
		}
	}

	nodes := make([]TopologyNode, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, *n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	data := TopologyData{Nodes: nodes, Edges: edges}
	b, _ := json.Marshal(data)
	return template.JS(b)
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

