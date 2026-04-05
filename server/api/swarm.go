// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
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

	// Agent-facing download endpoint (under /api/v1/agent/ prefix, no auth required).
	// Agents receive blob SHA256s via authenticated gRPC and download chunks here.
	mux.HandleFunc("GET /api/v1/agent/blob/{sha256}", h.Download)
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

// List returns all stored blobs.
func (h *SwarmHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	blobs, err := h.store.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "listing blobs: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blobs": blobs})
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
		SHA256 string `json:"sha256"`
		Target string `json:"target"`
		Label  string `json:"label"`
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

	assignment := swarm.AssignmentState{
		SHA256: req.SHA256,
		Size:   uint64(size),
		Label:  req.Label,
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
		// Try prefix resolution.
		if h.store != nil {
			if full, ok := h.store.Resolve(hash); ok {
				hash = full
			} else if swarm.IsValidHashPrefix(hash) {
				writeJSONError(w, http.StatusNotFound, "no blob matches prefix")
				return
			} else {
				writeJSONError(w, http.StatusBadRequest, "invalid sha256")
				return
			}
		} else {
			writeJSONError(w, http.StatusBadRequest, "invalid sha256")
			return
		}
	}

	// Get blob size to compute chunk count.
	var totalChunks int
	if h.store != nil {
		if f, size, err := h.store.Open(hash); err == nil {
			f.Close()
			totalChunks = chunksForSize(size)
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
		"label":       h.store.Label(hash),
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
