// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"time"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// SwarmHostnameResolver resolves envoy IDs to hostnames for display.
type SwarmHostnameResolver interface {
	ResolveHostname(envoyID string) string
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
	Source    string // "server" or "seed"
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

// SetSwarm configures the swarm blob store, peer tracker, and hostname resolver for the UI.
func (h *Handler) SetSwarm(store *swarm.BlobStore, tracker *swarm.PeerTracker, resolver SwarmHostnameResolver) {
	h.swarmStore = store
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

	seen := make(map[string]bool)
	var totalSize int64

	// Server-stored blobs.
	if h.swarmStore != nil {
		blobs, err := h.swarmStore.List()
		if err == nil {
			for _, b := range blobs {
				seen[b.SHA256] = true
				totalSize += b.Size
				entry := SwarmBlobEntry{
					SHA256:    b.SHA256,
					Label:     b.Label,
					Size:      b.Size,
					SizeHuman: humanSize(b.Size),
					Chunks:    b.Chunks,
					CreatedAt: b.CreatedAt,
					Source:    "server",
				}
				if h.swarmTracker != nil {
					statuses := h.swarmTracker.Status(b.SHA256, b.Chunks)
					entry.Summary = summarizeSwarmStatuses(statuses)
					if entry.Summary.Downloading > 0 || entry.Summary.Pending > 0 {
						data.ActiveDistributions++
					}
				}
				data.Blobs = append(data.Blobs, entry)
			}
		}
	}

	// Envoy-seeded blobs.
	if h.swarmTracker != nil {
		for sha, seed := range h.swarmTracker.ListSeeds() {
			if seen[sha] {
				continue
			}
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
				Source:    "seed",
			}
			statuses := h.swarmTracker.Status(sha, chunks)
			entry.Summary = summarizeSwarmStatuses(statuses)
			if entry.Summary.Downloading > 0 || entry.Summary.Pending > 0 {
				data.ActiveDistributions++
			}
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

	// Try server blob store first.
	if h.swarmStore != nil && h.swarmStore.Has(sha) {
		f, size, err := h.swarmStore.Open(sha)
		if err == nil {
			info, _ := f.Stat()
			f.Close()

			var createdAt time.Time
			if info != nil {
				createdAt = info.ModTime()
			}

			chunks := chunksForSize(size)
			detail = &SwarmBlobDetail{
				SHA256:    sha,
				Label:     h.swarmStore.Label(sha),
				Size:      size,
				SizeHuman: humanSize(size),
				Chunks:    chunks,
				CreatedAt: createdAt,
				Source:    "server",
			}
		}
	}

	// Fall back to seed.
	if detail == nil && h.swarmTracker != nil {
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
				Source:    "seed",
			}
		}
	}

	if detail == nil {
		http.NotFound(w, r)
		return
	}

	if h.swarmTracker != nil {
		statuses := h.swarmTracker.Status(sha, detail.Chunks)
		resolveSwarmHostnames(statuses, h.swarmFleetIndex)
		detail.Summary = summarizeSwarmStatuses(statuses)
		detail.Envoys = statuses
	}

	data.BlobDetail = detail
	data.TotalBlobs = 1

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

	// Determine blob size from store or seed.
	var size int64
	if h.swarmStore != nil {
		if f, sz, err := h.swarmStore.Open(sha); err == nil {
			f.Close()
			size = sz
		}
	}
	if size == 0 {
		if seed, ok := h.swarmTracker.GetSeed(sha); ok {
			size = int64(seed.Size)
		}
	}
	if size == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	chunks := chunksForSize(size)
	statuses := h.swarmTracker.Status(sha, chunks)

	resolveSwarmHostnames(statuses, h.swarmFleetIndex)

	detail := &SwarmBlobDetail{
		SHA256:  sha,
		Summary: summarizeSwarmStatuses(statuses),
		Envoys:  statuses,
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

func resolveSwarmHostnames(statuses []swarm.EnvoySwarmStatus, resolver SwarmHostnameResolver) {
	if resolver == nil {
		return
	}
	for i := range statuses {
		if h := resolver.ResolveHostname(statuses[i].Hostname); h != "" {
			statuses[i].Hostname = h
		}
	}
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
