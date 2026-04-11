// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// SwarmHostnameResolver resolves envoy IDs and IPs to hostnames for display.
type SwarmHostnameResolver interface {
	ResolveHostname(envoyID string) string
	ResolveIP(ip string) string
	// MatchingEnvoys returns hostnames of non-revoked envoys matching a target spec.
	MatchingEnvoys(target string) []string
}

// SwarmPageData provides data for the swarm blob list and detail pages.
type SwarmPageData struct {
	PageData
	TotalBlobs          int
	TotalSizeHuman      string
	ActiveDistributions int
	Blobs               []SwarmBlobEntry
	BlobDetail          *SwarmBlobDetail
}

// SwarmBlobEntry is a blob in the list view with distribution summary.
type SwarmBlobEntry struct {
	SHA256    string
	Label     string
	Size      int64
	SizeHuman string
	Chunks    int
	CreatedAt time.Time
	Source    string // hostname of the seeder or "seed"
	Status    string // "distributing", "complete", "retired"
	Retired   bool
	Summary   SwarmSummary
}

// SwarmBlobDetail is the detail view for a single blob.
type SwarmBlobDetail struct {
	SHA256    string
	Label     string
	Size      int64
	SizeHuman string
	Chunks    int
	CreatedAt time.Time
	Source    string // "server" or "seed"
	Retention string // human-readable retention (e.g. "7d", "" for none)
	Summary   SwarmSummary
	Envoys    []swarm.EnvoySwarmStatus
}

// SwarmSummary holds distribution counts.
type SwarmSummary struct {
	Complete    int
	Downloading int
	Pending     int
	Skipped     int
	Total       int
}

// SetSwarm configures the swarm peer tracker and hostname resolver for the UI.
func (h *Handler) SetSwarm(tracker *swarm.PeerTracker, resolver SwarmHostnameResolver) {
	h.swarmTracker = tracker
	h.swarmFleetIndex = resolver
}

// Swarm renders the blob list or blob detail page.
func (h *Handler) Swarm(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	if sha != "" {
		h.swarmDetail(w, r, sha)
		return
	}
	h.swarmList(w, r)
}

func (h *Handler) swarmList(w http.ResponseWriter, r *http.Request) {
	data := SwarmPageData{
		PageData: PageData{Title: "Swarm", ActiveNav: "swarm"},
	}

	var totalSize int64

	// Active seeds.
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

		// Retired seeds.
		for sha, seed := range h.swarmTracker.ListRetiredSeeds() {
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
				Retired:   true,
			}
			statuses := h.swarmTracker.Status(sha, chunks, seed.SeederID)
			entry.Summary = summarizeSwarmStatuses(statuses)
			entry.Status = "retired"
			data.Blobs = append(data.Blobs, entry)
		}
	}

	data.TotalBlobs = len(data.Blobs)
	data.TotalSizeHuman = humanSize(totalSize)
	if data.TotalSizeHuman == "" {
		data.TotalSizeHuman = "0 B"
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "swarm", data)
}

func (h *Handler) swarmDetail(w http.ResponseWriter, r *http.Request, sha string) {
	data := SwarmPageData{
		PageData: PageData{Title: "Swarm", ActiveNav: "swarm"},
	}

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
		target := ""
		if seed, ok := h.swarmTracker.GetSeed(sha); ok {
			seederID = seed.SeederID
			target = seed.Target
		}
		statuses := h.swarmTracker.Status(sha, detail.Chunks, seederID)
		resolveSwarmHostnames(statuses, h.swarmFleetIndex)
		addPendingEnvoys(&statuses, target, detail.Chunks, h.swarmFleetIndex)
		sortSwarmStatuses(statuses)
		detail.Summary = summarizeSwarmStatuses(statuses)
		detail.Envoys = statuses
	}

	data.BlobDetail = detail

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "swarm", data)
}

// SwarmEnvoysFragment serves the HTMX auto-refresh fragment for blob detail.
func (h *Handler) SwarmEnvoysFragment(w http.ResponseWriter, r *http.Request) {
	sha := r.URL.Query().Get("sha256")
	if sha == "" || h.swarmTracker == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var size int64
	var createdAt time.Time
	if seed, ok := h.swarmTracker.GetSeed(sha); ok {
		size = int64(seed.Size)
		createdAt = seed.DistributedAt
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
	target := ""
	if seed, ok := h.swarmTracker.GetSeed(sha); ok {
		target = seed.Target
	}
	statuses := h.swarmTracker.Status(sha, chunks, seederID)

	resolveSwarmHostnames(statuses, h.swarmFleetIndex)
	addPendingEnvoys(&statuses, target, chunks, h.swarmFleetIndex)
	sortSwarmStatuses(statuses)

	detail := &SwarmBlobDetail{
		SHA256:    sha,
		CreatedAt: createdAt,
		Summary:   summarizeSwarmStatuses(statuses),
		Envoys:    statuses,
	}

	h.renderer.RenderFragment(w, "swarm", "swarm-envoys-fragment", detail)
}

// chunksForSize returns the number of 1 MiB chunks for a blob.
func chunksForSize(size int64) int {
	if size <= 0 {
		return 0
	}
	n := int(size / int64(swarm.ChunkSize))
	if size%int64(swarm.ChunkSize) != 0 {
		n++
	}
	return n
}

func summarizeSwarmStatuses(statuses []swarm.EnvoySwarmStatus) SwarmSummary {
	var s SwarmSummary
	for _, st := range statuses {
		switch st.Status {
		case "complete":
			s.Complete++
		case "downloading":
			s.Downloading++
		case "pending":
			s.Pending++
		case "skipped":
			s.Skipped++
		}
	}
	s.Total = len(statuses)
	return s
}

// addPendingEnvoys adds envoys that match the target spec but haven't reported
// any status yet. With agent-driven self-assignment, the server doesn't track
// individual assignments — this fills the gap so the UI shows all expected envoys.
func addPendingEnvoys(statuses *[]swarm.EnvoySwarmStatus, target string, totalChunks int, resolver SwarmHostnameResolver) {
	if resolver == nil || target == "" {
		return
	}
	seen := make(map[string]bool, len(*statuses))
	for _, s := range *statuses {
		seen[s.Hostname] = true
	}
	for _, hostname := range resolver.MatchingEnvoys(target) {
		if seen[hostname] {
			continue
		}
		seen[hostname] = true
		*statuses = append(*statuses, swarm.EnvoySwarmStatus{
			Hostname:   hostname,
			Status:     "pending",
			ChunksNeed: totalChunks,
		})
	}
}

// sortSwarmStatuses sorts envoys: source first, then complete, downloading, pending, skipped.
// Within each group, sort by hostname for stable ordering across poll refreshes.
func sortSwarmStatuses(statuses []swarm.EnvoySwarmStatus) {
	statusRank := map[string]int{"complete": 1, "downloading": 2, "pending": 3, "skipped": 4}
	sort.SliceStable(statuses, func(i, j int) bool {
		// Source envoy always first.
		if statuses[i].IsSource != statuses[j].IsSource {
			return statuses[i].IsSource
		}
		ri, rj := statusRank[statuses[i].Status], statusRank[statuses[j].Status]
		if ri != rj {
			return ri < rj
		}
		return statuses[i].Hostname < statuses[j].Hostname
	})
}

func resolveSwarmHostnames(statuses []swarm.EnvoySwarmStatus, resolver SwarmHostnameResolver) {
	if resolver == nil {
		return
	}

	// Build IP→hostname lookup for chunk source resolution.
	ipToHost := make(map[string]string)

	for i := range statuses {
		if h := resolver.ResolveHostname(statuses[i].Hostname); h != "" {
			statuses[i].Hostname = h
		}

		// Resolve IP:port addresses in chunk sources to hostnames.
		if len(statuses[i].ChunkSources) > 0 {
			resolved := make(map[string]int, len(statuses[i].ChunkSources))
			for addr, count := range statuses[i].ChunkSources {
				if addr == "server" {
					resolved["server"] = count
					continue
				}
				// Try to resolve IP:port to hostname.
				ip := strings.SplitN(addr, ":", 2)[0]
				if host, ok := ipToHost[ip]; ok {
					resolved[host] = count
				} else if h := resolver.ResolveIP(ip); h != "" {
					ipToHost[ip] = h
					resolved[h] = count
				} else {
					resolved[addr] = count
				}
			}
			statuses[i].ChunkSources = resolved
		}

		// Also resolve IP:port in per-chunk detail sources.
		for j := range statuses[i].ChunkDetails {
			addr := statuses[i].ChunkDetails[j].Source
			if addr == "server" {
				continue
			}
			ip := strings.SplitN(addr, ":", 2)[0]
			if host, ok := ipToHost[ip]; ok {
				statuses[i].ChunkDetails[j].Source = host
			} else if h := resolver.ResolveIP(ip); h != "" {
				ipToHost[ip] = h
				statuses[i].ChunkDetails[j].Source = h
			}
		}
	}
}

// seedSourceName returns the seeder's hostname (e.g., "seed (danlap)") or "seed" if unresolvable.
func (h *Handler) seedSourceName(seederID string) string {
	if seederID != "" && h.swarmFleetIndex != nil {
		if name := h.swarmFleetIndex.ResolveHostname(seederID); name != "" {
			return name
		}
	}
	return "seed"
}

func formatRetention(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	days := int(d.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	return d.String()
}

func humanSize(bytes int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
