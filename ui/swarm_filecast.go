// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/i18n"
	"git.alexander4.io/vigo/server/swarm"
)

// SwarmHostnameResolver resolves envoy IDs and IPs to hostnames for display.
type SwarmHostnameResolver interface {
	ResolveHostname(envoyID string) string
	ResolveIP(ip string) string
	// MatchingEnvoys returns hostnames of non-revoked envoys matching a target spec.
	MatchingEnvoys(target string) []string
}

// FilecastPageData provides data for the admin-distributed-payload list and
// detail pages. Detail renders carry visualization data alongside BlobDetail
// so the single /payload/{sha256} page shows the heatmap + chunk-flow matrix
// directly.
//
// Gitback bundles (labels prefixed with `gitback:`) are filtered out at list
// assembly time — personal-DR repo mirrors have their own page at /gitback
// and don't belong in the admin-payload inventory.
type FilecastPageData struct {
	PageData
	Health              HealthBanner
	TotalBlobs          int
	TotalSizeHuman      string
	ActiveDistributions int
	Blobs               []FilecastBlobEntry
	BlobDetail          *FilecastBlobDetail

	// Visualization data — populated on the detail view.
	Matrix     *ChunkMatrix
	PeerColors map[string]string // hostname → hex color from swarmVizColors
	PeerTiers  map[string]int    // hostname → 0, 1, or 2
	VizCapped  bool              // true when visualizations show a subset of envoys
	VizTotal   int               // total envoy count when capped
}

// FilecastBlobEntry is a blob in the list view with distribution summary.
// Gitback bundles are filtered out before this struct is built — the
// /gitback page owns that surface.
type FilecastBlobEntry struct {
	SHA256    string
	Label     string
	Size      int64
	SizeHuman string
	Chunks    int
	CreatedAt time.Time
	Source    string // hostname of the seeder or "seed"
	Status    string // "distributing", "complete", "cached", "revoked"
	Revoked   bool
	Summary   SwarmSummary
}

// FilecastBlobDetail is the detail view for a single blob.
type FilecastBlobDetail struct {
	SHA256    string
	Label     string
	Size      int64
	SizeHuman string
	Chunks    int
	CreatedAt time.Time
	Source    string // "server" or "seed"
	Retention string // human-readable retention (e.g. "7d", "" for none)
	Revoked   bool   // drives the post-revoke summary layout in the template
	Summary   SwarmSummary
	Envoys    []swarm.EnvoySwarmStatus
}

// SwarmSummary holds distribution counts. Source/complete/downloading/pending/
// skipped apply when the blob is active. Revoking/revoked apply when the blob
// is revoked — the CLI and web UI pick which set to display based on Revoked.
// Source counts the seeder envoy; it has the blob by construction and is
// reported as a distinct status separate from "complete".
type SwarmSummary struct {
	Source      int
	Complete    int
	Cached      int
	Downloading int
	Pending     int
	Skipped     int
	Revoking    int
	Revoked     int
	Total       int
}

// SetSwarm configures the swarm peer tracker, hostname resolver, and swarm
// config for the UI.
func (h *Handler) SetSwarm(tracker *swarm.PeerTracker, resolver SwarmHostnameResolver, swarmCfg config.SwarmSection) {
	h.swarmTracker = tracker
	h.swarmFleetIndex = resolver
	h.swarmConfig = swarmCfg
}

// Filecast renders the admin-distributed filecast list or detail page.
func (h *Handler) Filecast(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	if sha != "" {
		h.filecastDetail(w, r, sha)
		return
	}
	h.filecastList(w, r)
}

func (h *Handler) filecastList(w http.ResponseWriter, r *http.Request) {
	data := FilecastPageData{
		PageData: PageData{Title: "Filecast", ActiveNav: "filecast"},
	}

	var totalSize int64

	if h.swarmTracker != nil {
		for sha, seed := range h.swarmTracker.ListSeeds() {
			if strings.HasPrefix(seed.Label, "gitback:") {
				continue
			}
			size := int64(seed.Size)
			totalSize += size
			chunks := chunksForSize(size)
			entry := FilecastBlobEntry{
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

		// Revoked seeds (gitback bundles still skipped — rotation is a
		// gitback-internal concern, not an admin-payload one).
		for sha, seed := range h.swarmTracker.ListRevokedSeeds() {
			if strings.HasPrefix(seed.Label, "gitback:") {
				continue
			}
			size := int64(seed.Size)
			chunks := chunksForSize(size)
			entry := FilecastBlobEntry{
				SHA256:    sha,
				Label:     seed.Label,
				Size:      size,
				SizeHuman: humanSize(size),
				Chunks:    chunks,
				CreatedAt: seed.DistributedAt,
				Source:    h.seedSourceName(seed.SeederID),
				Revoked:   true,
			}
			statuses := h.swarmTracker.Status(sha, chunks, seed.SeederID)
			entry.Summary = summarizeSwarmStatuses(statuses)
			entry.Status = "revoked"
			data.Blobs = append(data.Blobs, entry)
		}
	}

	data.Health = buildFilecastHealth(h.localeFromRequest(r), data.Blobs)

	data.TotalBlobs = len(data.Blobs)
	data.TotalSizeHuman = humanSize(totalSize)
	if data.TotalSizeHuman == "" {
		data.TotalSizeHuman = "0 B"
	}

	h.setCommonWithRequest(&data.PageData, r)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "filecast", data)
}

func (h *Handler) filecastDetail(w http.ResponseWriter, r *http.Request, sha string) {
	data := FilecastPageData{
		PageData: PageData{Title: "Filecast", ActiveNav: "filecast"},
	}

	var detail *FilecastBlobDetail

	if h.swarmTracker != nil {
		if seed, ok := h.swarmTracker.GetSeed(sha); ok {
			// Gitback bundles belong on /gitback, not /payload. Redirect so
			// admins pasting a gitback sha land on the right page.
			if strings.HasPrefix(seed.Label, "gitback:") {
				http.Redirect(w, r, "/swarm/gitback/"+seed.Label, http.StatusSeeOther)
				return
			}
			size := int64(seed.Size)
			chunks := chunksForSize(size)
			detail = &FilecastBlobDetail{
				SHA256:    sha,
				Label:     seed.Label,
				Size:      size,
				SizeHuman: humanSize(size),
				Chunks:    chunks,
				CreatedAt: seed.DistributedAt,
				Source:    h.seedSourceName(seed.SeederID),
				Retention: formatRetention(seed.Retention),
				Revoked:   seed.Revoked,
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
		// Skip pending fan-out on revoked seeds — pending rows are irrelevant
		// once the blob is being evicted.
		if !detail.Revoked {
			addPendingEnvoys(&statuses, detail.Label, target, detail.Chunks, h.swarmFleetIndex)
		}
		sortSwarmStatuses(statuses)
		detail.Summary = summarizeSwarmStatuses(statuses)
		detail.Envoys = statuses

		h.populateVizData(&data, detail.Envoys, detail.Chunks)
	}

	data.BlobDetail = detail

	h.setCommonWithRequest(&data.PageData, r)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "filecast", data)
}

// populateVizData computes the matrix + palette for the detail view.
// Extracted so the full-page render and the HTMX fragment stay in lockstep.
func (h *Handler) populateVizData(data *FilecastPageData, envoys []swarm.EnvoySwarmStatus, chunks int) {
	vizEnvoys := topVizEnvoys(envoys, maxVizEnvoys)
	if len(envoys) > maxVizEnvoys {
		data.VizCapped = true
		data.VizTotal = len(envoys)
	}
	var tagLookup func(string) []string
	if h.traitLookup != nil {
		tagLookup = h.traitLookup.EnvoyTagsByHostname
	}
	data.PeerColors, data.PeerTiers = buildPeerVisualMap(vizEnvoys, tagLookup)
	data.Matrix = buildChunkMatrix(vizEnvoys, data.PeerColors)
}

// LodeEnvoysFragment serves the HTMX auto-refresh fragment for blob detail.
func (h *Handler) FilecastEnvoysFragment(w http.ResponseWriter, r *http.Request) {
	sha := r.URL.Query().Get("sha256")
	if sha == "" || h.swarmTracker == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var size int64
	var createdAt time.Time
	var seederID, target, label string
	var revoked bool
	if seed, ok := h.swarmTracker.GetSeed(sha); ok {
		size = int64(seed.Size)
		createdAt = seed.DistributedAt
		seederID = seed.SeederID
		target = seed.Target
		label = seed.Label
		revoked = seed.Revoked
	}
	if size == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	chunks := chunksForSize(size)
	statuses := h.swarmTracker.Status(sha, chunks, seederID)

	resolveSwarmHostnames(statuses, h.swarmFleetIndex)
	if !revoked {
		addPendingEnvoys(&statuses, label, target, chunks, h.swarmFleetIndex)
	}
	sortSwarmStatuses(statuses)

	detail := &FilecastBlobDetail{
		SHA256:    sha,
		Chunks:    chunks,
		CreatedAt: createdAt,
		Revoked:   revoked,
		Summary:   summarizeSwarmStatuses(statuses),
		Envoys:    statuses,
	}

	// The fragment template expects the full FilecastPageData shape so its viz
	// sub-sections can render. Build one populated with the detail + viz data.
	data := FilecastPageData{BlobDetail: detail}
	h.populateVizData(&data, statuses, chunks)

	data.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "filecast", "filecast-envoys-fragment", data)
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
		case "source":
			s.Source++
		case "complete":
			s.Complete++
		case "cached":
			s.Cached++
		case "downloading":
			s.Downloading++
		case "pending":
			s.Pending++
		case "skipped":
			s.Skipped++
		case "revoking":
			s.Revoking++
		case "revoked":
			s.Revoked++
		}
	}
	s.Total = len(statuses) - s.Source
	return s
}

// addPendingEnvoys adds envoys that should eventually have the blob
// but haven't reported chunks yet. Fan-out uses the target pattern
// against the full fleet.
//
// Claim-only labels (gitback) skip the fan-out entirely: as of
// Phase 8 (0.23.0) the server no longer tracks which envoys hold a
// gitback mirror — selection is entirely P2P. Operators view
// gitback state via `vigo repo list` on individual envoys or via
// the `gitback.sources[]` trait payload.
func addPendingEnvoys(statuses *[]swarm.EnvoySwarmStatus, label, target string, totalChunks int, resolver SwarmHostnameResolver) {
	if resolver == nil {
		return
	}
	if swarm.IsClaimOnlyLabel(label) {
		return
	}
	seen := make(map[string]bool, len(*statuses))
	for _, s := range *statuses {
		seen[s.Hostname] = true
	}

	if target == "" {
		return
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
	// Source is rank 0 so it sorts ahead of complete (1). No separate
	// IsSource check — the status value itself is authoritative.
	statusRank := map[string]int{"source": 0, "complete": 1, "downloading": 2, "pending": 3, "skipped": 4}
	sort.SliceStable(statuses, func(i, j int) bool {
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
				if addr == "server" || addr == "cached" {
					resolved[addr] = count
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
			if addr == "server" || addr == "cached" {
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

// buildFilecastHealth surfaces distribution problems on the list view.
// Skipped > 0 means the publisher matched targets that couldn't
// receive (out of disk, unreachable); operators need to see that.
// Pending is usually in-flight and not a problem by itself, so it's
// left informational. Revoking/revoked counts are cleanup, not a
// health concern.
func buildFilecastHealth(locale string, blobs []FilecastBlobEntry) HealthBanner {
	var problems []HealthProblem
	var active int
	for _, b := range blobs {
		if b.Revoked {
			continue
		}
		active++
		if b.Summary.Skipped > 0 {
			label := b.Label
			if label == "" {
				label = shortSha(b.SHA256)
			}
			problems = append(problems, HealthProblem{
				Kind:     "skipped",
				Subject:  label,
				Detail:   fmt.Sprintf(i18n.CatalogT(locale, "swarm.filecast.skipped_detail"), b.Summary.Skipped),
				Link:     "/swarm/filecast/" + b.SHA256,
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf(i18n.CatalogT(locale, "swarm.filecast.headline"),
		active, len(blobs)-active)
	return finalizeHealthBanner("Filecast", problems, headline)
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
