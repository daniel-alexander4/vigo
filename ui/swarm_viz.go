// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"encoding/json"
	"hash/fnv"
	"html/template"
	"net/http"
	"sort"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// Maximum columns in the chunk heatmap. Larger files are bucketed.
const maxHeatmapColumns = 200

// Maximum envoys shown in heatmap/topology/timeline visualizations.
const maxVizEnvoys = 50


// SwarmVizPageData extends SwarmPageData with visualization data.
//
// Peer identity is represented by three orthogonal dimensions hashed from
// the hostname: color (drawn everywhere), shape (topology dots), and dash
// pattern (timeline polylines). All three are always present — no adaptive
// mode switching — so the visual grammar of the page is identical at any
// fleet size. See buildPeerVisualMap.
type SwarmVizPageData struct {
	SwarmPageData
	HeatmapRows    []HeatmapRow
	HeatmapColumns int
	TopologyJSON   template.JS
	DurationBars   []DurationBar
	PeerColors     map[string]string // hostname → Catppuccin color
	PeerShapes     map[string]string // hostname → shape name (circle, square, triangle, diamond, star)
	PeerDashes     map[string]string // hostname → SVG stroke-dasharray string ("" for solid)
	VizCapped      bool              // true when visualizations show a subset of envoys
	VizTotal       int               // total envoy count when capped
}

// HeatmapRow is one envoy's row in the chunk distribution heatmap.
type HeatmapRow struct {
	Hostname string
	Status   string // overall status: complete, downloading, pending, skipped
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
// Color is the hostname's assigned Catppuccin color from PeerColors; it's
// used as the fill in the D3 node. Shape is the symbol name (circle, square,
// triangle, diamond, star) rendered via d3.symbol(). Status drives the stroke
// color so one glance tells the operator "who is this" (color + shape) and
// "what state is it in" (stroke ring).
type TopologyNode struct {
	ID       string `json:"id"`
	IsSource bool   `json:"isSource"`
	Status   string `json:"status"`
	Chunks   int    `json:"chunks"`
	Color    string `json:"color"`
	Shape    string `json:"shape"`
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

	// Cap visualizations to 50 envoys for readability and performance.
	// Show all envoys in the text table, but only the most interesting
	// in heatmap/topology/timeline (downloading/pending first, then by name).
	vizEnvoys := detail.Envoys
	if len(vizEnvoys) > maxVizEnvoys {
		vizEnvoys = topVizEnvoys(vizEnvoys, maxVizEnvoys)
		data.VizCapped = true
		data.VizTotal = len(detail.Envoys)
	}

	data.PeerColors, data.PeerShapes, data.PeerDashes = buildPeerVisualMap(detail.Envoys)
	data.HeatmapColumns = min(detail.Chunks, maxHeatmapColumns)
	data.HeatmapRows = buildHeatmapRows(vizEnvoys, detail.Chunks, data.HeatmapColumns)
	data.TopologyJSON = buildTopologyJSON(vizEnvoys, data.PeerColors, data.PeerShapes)
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

	// Cap visualizations to 50 envoys for readability and performance.
	vizEnvoys := statuses
	vizCapped := false
	vizTotal := len(statuses)
	if len(vizEnvoys) > maxVizEnvoys {
		vizEnvoys = topVizEnvoys(vizEnvoys, maxVizEnvoys)
		vizCapped = true
	}

	cols := min(chunks, maxHeatmapColumns)
	peerColors, peerShapes, peerDashes := buildPeerVisualMap(statuses)

	vizData := &SwarmVizPageData{
		HeatmapRows:    buildHeatmapRows(vizEnvoys, chunks, cols),
		HeatmapColumns: cols,
		TopologyJSON:   buildTopologyJSON(vizEnvoys, peerColors, peerShapes),
		DurationBars:   buildDurationBars(vizEnvoys, peerColors),
		PeerColors:     peerColors,
		PeerShapes:     peerShapes,
		PeerDashes:     peerDashes,
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
		if envoy.Status == "complete" || (total > 0 && have == total) {
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

// buildTopologyJSON creates the D3 graph data. peerColors and peerShapes
// are the hostname→visual maps built by buildPeerVisualMap; each node's
// fill comes from peerColors and its symbol type comes from peerShapes so
// every envoy has a stable (color, shape) identity matching the rest of
// the page.
func buildTopologyJSON(envoys []swarm.EnvoySwarmStatus, peerColors, peerShapes map[string]string) template.JS {
	nodeMap := make(map[string]*TopologyNode)
	var edges []TopologyEdge

	for _, envoy := range envoys {
		if _, ok := nodeMap[envoy.Hostname]; !ok {
			nodeMap[envoy.Hostname] = &TopologyNode{
				ID:       envoy.Hostname,
				IsSource: envoy.IsSource,
				Status:   envoy.Status,
				Chunks:   envoy.ChunksHave,
				Color:    peerColors[envoy.Hostname],
				Shape:    peerShapes[envoy.Hostname],
			}
		}

		for src, count := range envoy.ChunkSources {
			if src == envoy.Hostname || src == "cached" {
				continue
			}
			// Ensure source node exists.
			if _, ok := nodeMap[src]; !ok {
				nodeMap[src] = &TopologyNode{
					ID:     src,
					Status: "complete", // if it's serving chunks, it has them
					Color:  peerColors[src],
					Shape:  peerShapes[src],
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

// buildPeerVisualMap assigns deterministic (color, shape, dash) triples to
// peer hostnames using FNV-32a hash with different bit ranges per dimension
// so the three dimensions are uncorrelated. Every envoy always has all three;
// the viz renders whichever it needs per section (color everywhere, shape in
// topology, dash in timeline).
//
// Returns three parallel maps keyed by hostname. Peers are collected from
// both envoy hostnames and chunk sources so cross-subnet seeders without a
// direct envoy entry still get identities.
func buildPeerVisualMap(envoys []swarm.EnvoySwarmStatus) (colors, shapes, dashes map[string]string) {
	colors = make(map[string]string)
	shapes = make(map[string]string)
	dashes = make(map[string]string)

	// Collect all unique peer names from envoys + their chunk sources.
	peers := make(map[string]bool)
	for _, envoy := range envoys {
		peers[envoy.Hostname] = true
		for src := range envoy.ChunkSources {
			peers[src] = true
		}
	}

	// Hash-based assignment. Using FNV-32a with different bit ranges for
	// each dimension gives near-independent distributions — two envoys
	// that collide on color are unlikely to also collide on shape.
	for peer := range peers {
		h := fnv.New32a()
		h.Write([]byte(peer))
		sum := h.Sum32()

		colorIdx := int((sum>>0)&0xFF) % len(VizColorPalette)
		shapeIdx := int((sum>>8)&0xFF) % len(VizShapePalette)
		dashIdx := int((sum>>16)&0xFF) % len(VizDashPalette)

		colors[peer] = VizColorPalette[colorIdx]
		shapes[peer] = VizShapePalette[shapeIdx]
		dashes[peer] = VizDashPalette[dashIdx]
	}
	return colors, shapes, dashes
}

// topVizEnvoys picks the most interesting envoys for visualization:
// downloading first, then pending, then complete — sorted by hostname within each group.
func topVizEnvoys(envoys []swarm.EnvoySwarmStatus, n int) []swarm.EnvoySwarmStatus {
	cp := make([]swarm.EnvoySwarmStatus, len(envoys))
	copy(cp, envoys)

	sort.SliceStable(cp, func(i, j int) bool {
		pi, pj := vizStatusPriority(cp[i].Status), vizStatusPriority(cp[j].Status)
		if pi != pj {
			return pi < pj
		}
		return cp[i].Hostname < cp[j].Hostname
	})
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}

func vizStatusPriority(s string) int {
	switch s {
	case "downloading":
		return 0
	case "pending":
		return 1
	case "skipped":
		return 2
	case "complete":
		return 3
	default:
		return 4
	}
}

