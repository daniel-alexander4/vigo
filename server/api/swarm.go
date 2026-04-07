// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/swarm"
)

// SwarmForcePusher triggers a force-push to streaming agents so they
// send a fresh CheckIn and receive pending swarm assignments.
type SwarmForcePusher interface {
	ForcePush(envoyID string) int
}

// SwarmHandler handles blob storage and distribution REST endpoints.
type SwarmHandler struct {
	store       *swarm.BlobStore
	tracker     *swarm.PeerTracker
	fleetIndex  *fleet.FleetIndex
	forcePusher SwarmForcePusher
}

// NewSwarmHandler creates a new swarm handler.
func NewSwarmHandler(store *swarm.BlobStore, tracker *swarm.PeerTracker, fleetIndex *fleet.FleetIndex) *SwarmHandler {
	return &SwarmHandler{
		store:      store,
		tracker:    tracker,
		fleetIndex: fleetIndex,
	}
}

// SetForcePusher wires the stream manager's force push capability so that
// distribute triggers a fresh check-in from streaming agents.
func (h *SwarmHandler) SetForcePusher(fp SwarmForcePusher) {
	h.forcePusher = fp
}

// RegisterRoutes registers swarm blob endpoints.
func (h *SwarmHandler) RegisterRoutes(mux *http.ServeMux) {
	// Admin endpoints (require auth).
	mux.HandleFunc("POST /api/v1/blob", h.Upload)
	mux.HandleFunc("GET /api/v1/blob", h.List)
	mux.HandleFunc("GET /api/v1/blob/{sha256}", h.Download)
	mux.HandleFunc("GET /api/v1/blob/{sha256}/meta", h.Meta)
	mux.HandleFunc("DELETE /api/v1/blob/{sha256}", h.Delete)
	mux.HandleFunc("POST /api/v1/swarm/distribute", h.Distribute)
	mux.HandleFunc("GET /api/v1/swarm/status/{sha256}", h.Status)
	mux.HandleFunc("GET /api/v1/swarm/broken", h.BrokenSeeds)

	// Agent-facing blob download (separate from /api/v1/agent/ to avoid
	// conflicts with the agent binary update path).
	mux.HandleFunc("GET /api/v1/swarm/blob/{sha256}", h.Download)
}

// Upload stores a blob from the request body.
func (h *SwarmHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	label := r.URL.Query().Get("label")
	if label == "" {
		label = r.Header.Get("X-Blob-Label")
	}

	hash, size, err := h.store.Store(r.Body, label)
	if err != nil {
		slog.Error("blob upload failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "storing blob: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"sha256":      hash,
		"size":        size,
		"label":       label,
		"chunk_size":  swarm.ChunkSize,
		"chunk_count": chunksForSize(size),
	})
}

// Download serves a blob with HTTP Range request support.
func (h *SwarmHandler) Download(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	hash := h.resolveHash(r.PathValue("sha256"))
	f, size, err := h.store.Open(hash)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "blob not found")
		return
	}
	defer f.Close()
	_ = size // used by ServeContent internally

	w.Header().Set("X-Blob-Label", h.store.Label(hash))

	info, _ := f.Stat()
	var modTime time.Time
	if info != nil {
		modTime = info.ModTime()
	}

	// http.ServeContent handles Range headers, 206 Partial Content, and Content-Type.
	http.ServeContent(w, r, hash, modTime, f)
}

// Meta returns blob metadata (size, chunk count, chunk size).
func (h *SwarmHandler) Meta(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	hash := h.resolveHash(r.PathValue("sha256"))
	f, size, err := h.store.Open(hash)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "blob not found")
		return
	}
	f.Close()

	writeJSON(w, http.StatusOK, map[string]any{
		"sha256":      hash,
		"size":        size,
		"label":       h.store.Label(hash),
		"chunk_size":  swarm.ChunkSize,
		"chunk_count": chunksForSize(size),
	})
}

// swarmEntry is a unified blob/seed entry for the list API.
type swarmEntry struct {
	SHA256      string `json:"sha256"`
	Label       string `json:"label"`
	Size        int64  `json:"size"`
	Source      string `json:"source"` // "server" or "seed"
	Target      string `json:"target,omitempty"`
	Retention   string `json:"retention,omitempty"`
	Complete    int    `json:"complete"`
	Downloading int    `json:"downloading"`
	Pending     int    `json:"pending"`
	Total       int    `json:"total"`
	CreatedAt   string `json:"created_at,omitempty"`
	Retired     bool   `json:"retired,omitempty"`
	RetiredAt   string `json:"retired_at,omitempty"`
}

// List returns all swarm entries (server blobs + envoy seeds) with status summaries.
func (h *SwarmHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	var entries []swarmEntry
	seen := make(map[string]bool)

	// Server-stored blobs.
	if h.store != nil {
		blobs, err := h.store.List()
		if err == nil {
			for _, b := range blobs {
				seen[b.SHA256] = true
				c, d, p, t := h.tracker.StatusSummary(b.SHA256)
				entries = append(entries, swarmEntry{
					SHA256:      b.SHA256,
					Label:       b.Label,
					Size:        b.Size,
					Source:      "server",
					Complete:    c,
					Downloading: d,
					Pending:     p,
					Total:       t,
					CreatedAt:   b.CreatedAt.Format("2006-01-02T15:04:05Z"),
				})
			}
		}
	}

	// Envoy-seeded blobs (active).
	for sha256, seed := range h.tracker.ListSeeds() {
		if seen[sha256] {
			continue
		}
		entries = append(entries, seedToEntry(sha256, seed, h.tracker))
	}

	// Include retired seeds if requested.
	if r.URL.Query().Get("retired") == "true" {
		for sha256, seed := range h.tracker.ListRetiredSeeds() {
			if seen[sha256] {
				continue
			}
			entries = append(entries, seedToEntry(sha256, seed, h.tracker))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"blobs": entries})
}

func seedToEntry(sha256 string, seed swarm.SeedState, tracker *swarm.PeerTracker) swarmEntry {
	c, d, p, t := tracker.StatusSummary(sha256)
	var ret string
	if seed.Retention > 0 {
		days := int(seed.Retention.Hours() / 24)
		if days > 0 {
			ret = fmt.Sprintf("%dd", days)
		} else {
			ret = seed.Retention.String()
		}
	}
	e := swarmEntry{
		SHA256:      sha256,
		Label:       seed.Label,
		Size:        int64(seed.Size),
		Source:      "seed",
		Target:      seed.Target,
		Retention:   ret,
		Complete:    c,
		Downloading: d,
		Pending:     p,
		Total:       t,
		CreatedAt:   seed.DistributedAt.Format("2006-01-02T15:04:05Z"),
	}
	if seed.Retired {
		e.Retired = true
		e.RetiredAt = seed.RetiredAt.Format("2006-01-02T15:04:05Z")
	}
	return e
}

// Delete removes a blob.
func (h *SwarmHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	hash := h.resolveHash(r.PathValue("sha256"))
	if err := h.store.Delete(hash); err != nil {
		writeJSONError(w, http.StatusNotFound, "blob not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": hash})
}

// Distribute assigns a blob to envoys matching a target spec.
func (h *SwarmHandler) Distribute(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.tracker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	var req struct {
		SHA256    string `json:"sha256"`
		Target    string `json:"target"`
		Label     string `json:"label"`
		Retention string `json:"retention"` // e.g. "7d", "168h", "" for no auto-eviction
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.SHA256 == "" {
		writeJSONError(w, http.StatusBadRequest, "sha256 required")
		return
	}
	if !h.store.Has(req.SHA256) {
		writeJSONError(w, http.StatusNotFound, "blob not found — upload it first via POST /api/v1/blob")
		return
	}

	if req.Label != "" {
		h.store.SetLabel(req.SHA256, req.Label)
	}

	// Get blob size for the assignment.
	f, size, err := h.store.Open(req.SHA256)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "reading blob: "+err.Error())
		return
	}
	f.Close()

	var retention time.Duration
	if req.Retention != "" {
		retention = parseSwarmRetention(req.Retention)
	}

	assignment := swarm.AssignmentState{
		SHA256:    req.SHA256,
		Size:      uint64(size),
		Label:     req.Label,
		Retention: retention,
	}

	// Resolve targets using FleetIndex. Supports hostname globs (*.web.*),
	// trait filters (os.distro=ubuntu), and "*" for all envoys.
	var targetCount int
	if h.fleetIndex != nil {
		target := req.Target
		if target == "" {
			target = "*"
		}
		for _, envoy := range h.fleetIndex.AllEnvoys() {
			if envoy.Revoked {
				continue
			}
			if matchSwarmTarget(envoy, target) {
				h.tracker.Assign(envoy.ID, assignment)
				targetCount++
			}
		}
	}

	// Force-push streaming agents so they send a fresh CheckIn and
	// receive the new swarm assignments immediately.
	if h.forcePusher != nil && targetCount > 0 {
		// Push all — simpler than tracking individual envoy IDs, and
		// non-targeted agents just get a no-op re-check.
		h.forcePusher.ForcePush("")
	}

	slog.Info("swarm distribute",
		"sha256", req.SHA256[:12],
		"label", req.Label,
		"targets", targetCount,
		"size", size,
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"sha256":  req.SHA256,
		"label":   req.Label,
		"targets": targetCount,
		"size":    size,
	})
}

// Status returns per-envoy distribution progress for a blob.
func (h *SwarmHandler) Status(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	hash := r.PathValue("sha256")
	if !swarm.IsValidHash(hash) {
		// Try prefix resolution — check blob store first, then seed registry.
		resolved := false
		if h.store != nil {
			if full, ok := h.store.Resolve(hash); ok {
				hash = full
				resolved = true
			}
		}
		if !resolved {
			if full, ok := h.tracker.ResolveSeedPrefix(hash); ok {
				hash = full
				resolved = true
			}
		}
		if !resolved {
			if swarm.IsValidHashPrefix(hash) {
				writeJSONError(w, http.StatusNotFound, "no blob matches prefix")
			} else {
				writeJSONError(w, http.StatusBadRequest, "invalid sha256")
			}
			return
		}
	}

	// Get blob size to compute chunk count — check store first, fall back to seed registry.
	var totalChunks int
	var label string
	if h.store != nil {
		if f, size, err := h.store.Open(hash); err == nil {
			f.Close()
			totalChunks = chunksForSize(size)
			label = h.store.Label(hash)
		}
	}
	if totalChunks == 0 {
		if seed, ok := h.tracker.GetSeed(hash); ok {
			totalChunks = chunksForSize(int64(seed.Size))
			label = seed.Label
		}
	}

	statuses := h.tracker.Status(hash, totalChunks)

	// Resolve envoy IDs to hostnames.
	if h.fleetIndex != nil {
		for i := range statuses {
			if state := h.fleetIndex.GetEnvoy(statuses[i].Hostname); state != nil {
				statuses[i].Hostname = state.Hostname
			}
		}
	}

	// Compute summary counts.
	var complete, downloading, pending, skipped int
	for _, s := range statuses {
		switch s.Status {
		case "complete":
			complete++
		case "downloading":
			downloading++
		case "pending":
			pending++
		case "skipped":
			skipped++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sha256":      hash,
		"label":       label,
		"chunk_count": totalChunks,
		"summary": map[string]int{
			"complete":    complete,
			"downloading": downloading,
			"pending":     pending,
			"skipped":     skipped,
			"total":       len(statuses),
		},
		"envoys": statuses,
	})
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

// matchSwarmTarget checks whether an envoy matches a target spec.
// Supports: "*" (all), hostname globs ("*.web.*"), trait filters ("key=value").
// parseSwarmRetention parses "7d" or "168h" style duration strings.
func parseSwarmRetention(s string) time.Duration {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	d, _ := time.ParseDuration(s)
	return d
}

func matchSwarmTarget(envoy fleet.EnvoyState, spec string) bool {
	if spec == "*" {
		return true
	}
	// Trait filter: key=value
	if strings.Contains(spec, "=") {
		parts := strings.SplitN(spec, "=", 2)
		if len(parts) == 2 {
			return envoy.Traits[parts[0]] == parts[1]
		}
	}
	// Hostname glob
	if matched, _ := filepath.Match(spec, envoy.Hostname); matched {
		return true
	}
	// Exact hostname match
	return envoy.Hostname == spec
}

// BrokenSeeds returns broken seed manifests across all envoys.
func (h *SwarmHandler) BrokenSeeds(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	type envoyBroken struct {
		Hostname    string             `json:"hostname"`
		BrokenSeeds []swarm.BrokenSeed `json:"broken_seeds"`
	}

	all := h.tracker.AllBrokenSeeds()
	var envoys []envoyBroken
	for envoyID, broken := range all {
		hostname := envoyID
		if h.fleetIndex != nil {
			if es := h.fleetIndex.GetEnvoy(envoyID); es != nil {
				hostname = es.Hostname
			}
		}
		envoys = append(envoys, envoyBroken{
			Hostname:    hostname,
			BrokenSeeds: broken,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"envoys": envoys})
}

// resolveHash expands a hash or unique prefix to the full SHA256 via the blob store.
// Returns the input unchanged if it's already a full hash or resolution fails.
func (h *SwarmHandler) resolveHash(hashOrPrefix string) string {
	if swarm.IsValidHash(hashOrPrefix) {
		return hashOrPrefix
	}
	if h.store != nil {
		if full, ok := h.store.Resolve(hashOrPrefix); ok {
			return full
		}
	}
	return hashOrPrefix
}
