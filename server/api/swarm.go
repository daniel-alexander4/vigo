// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
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

// SwarmHandler handles swarm distribution REST endpoints.
type SwarmHandler struct {
	seedDir       string
	tracker       *swarm.PeerTracker
	fleetIndex    *fleet.FleetIndex
	forcePusher   SwarmForcePusher
	manifestStore *swarm.ManifestStore
}

// NewSwarmHandler creates a new swarm handler.
func NewSwarmHandler(seedDir string, tracker *swarm.PeerTracker, fleetIndex *fleet.FleetIndex, manifest *swarm.ManifestStore) *SwarmHandler {
	return &SwarmHandler{
		seedDir:       seedDir,
		tracker:       tracker,
		fleetIndex:    fleetIndex,
		manifestStore: manifest,
	}
}

// SetForcePusher wires the stream manager's force push capability so that
// distribute triggers a fresh check-in from streaming agents.
func (h *SwarmHandler) SetForcePusher(fp SwarmForcePusher) {
	h.forcePusher = fp
}

// RegisterRoutes registers swarm endpoints.
func (h *SwarmHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/swarm/distribute", h.Distribute)
	mux.HandleFunc("GET /api/v1/swarm/list", h.List)
	mux.HandleFunc("GET /api/v1/swarm/status/{sha256}", h.Status)
	mux.HandleFunc("DELETE /api/v1/swarm/{sha256}", h.Delete)
	mux.HandleFunc("GET /api/v1/swarm/broken", h.BrokenSeeds)
	mux.HandleFunc("GET /api/v1/swarm/match", h.Match)
}

// Distribute accepts a file upload and places it in the server's seed directory
// with a .swarm manifest. The server's own agent discovers it on the next
// check-in via the swarm_seeds trait collector and auto-distributes to targets.
func (h *SwarmHandler) Distribute(w http.ResponseWriter, r *http.Request) {
	if h.seedDir == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	label := r.Header.Get("X-Swarm-Label")
	target := r.Header.Get("X-Swarm-Target")
	retention := r.Header.Get("X-Swarm-Retention")

	if label == "" {
		writeJSONError(w, http.StatusBadRequest, "X-Swarm-Label header required")
		return
	}
	if target == "" {
		target = "*"
	}
	if retention == "" {
		retention = "7d"
	}

	// Sanitize label — no path separators allowed.
	label = filepath.Base(label)

	// Write manifest FIRST to avoid race with trait collector seeing
	// the file before the manifest exists (would report as broken seed).
	manifestPath := filepath.Join(h.seedDir, label+".swarm")
	manifest := fmt.Sprintf(`{"target":%q,"retention":%q}`, target, retention)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "writing manifest: "+err.Error())
		return
	}

	// Stream file body to temp file while computing SHA256.
	tmp, err := os.CreateTemp(h.seedDir, ".upload-*")
	if err != nil {
		os.Remove(manifestPath)
		writeJSONError(w, http.StatusInternalServerError, "creating temp file: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath) // no-op if already renamed
	}()

	hasher := sha256.New()
	mw := io.MultiWriter(tmp, hasher)
	size, err := io.Copy(mw, r.Body)
	if err != nil {
		os.Remove(manifestPath)
		writeJSONError(w, http.StatusInternalServerError, "writing file: "+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(manifestPath)
		writeJSONError(w, http.StatusInternalServerError, "closing temp file: "+err.Error())
		return
	}

	hash := hex.EncodeToString(hasher.Sum(nil))

	// Move to final path.
	destPath := filepath.Join(h.seedDir, label)
	if err := os.Rename(tmpPath, destPath); err != nil {
		// Cross-device: copy + delete.
		if copyErr := copyFile(tmpPath, destPath); copyErr != nil {
			os.Remove(manifestPath)
			writeJSONError(w, http.StatusInternalServerError, "moving file: "+copyErr.Error())
			return
		}
	}

	slog.Info("swarm seed placed",
		"sha256", hash[:12],
		"label", label,
		"target", target,
		"retention", retention,
		"size", size,
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"sha256": hash,
		"label":  label,
		"size":   size,
	})
}

// swarmEntry is a unified seed entry for the list API.
type swarmEntry struct {
	SHA256      string `json:"sha256"`
	Label       string `json:"label"`
	Size        int64  `json:"size"`
	Source      string `json:"source"`
	Target      string `json:"target,omitempty"`
	Retention   string `json:"retention,omitempty"`
	Complete    int    `json:"complete"`
	Downloading int    `json:"downloading"`
	Pending     int    `json:"pending"`
	Skipped     int    `json:"skipped"`
	Total       int    `json:"total"`
	CreatedAt   string `json:"created_at,omitempty"`
	Retired     bool   `json:"retired,omitempty"`
	RetiredAt   string `json:"retired_at,omitempty"`
}

// List returns all swarm seeds with status summaries.
func (h *SwarmHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	var entries []swarmEntry

	// Active seeds.
	for sha256, seed := range h.tracker.ListSeeds() {
		entries = append(entries, seedToEntry(sha256, seed, h.tracker, h.fleetIndex))
	}

	// Include retired seeds if requested.
	if r.URL.Query().Get("retired") == "true" {
		for sha256, seed := range h.tracker.ListRetiredSeeds() {
			entries = append(entries, seedToEntry(sha256, seed, h.tracker, h.fleetIndex))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"blobs": entries})
}

func seedToEntry(sha256 string, seed swarm.SeedState, tracker *swarm.PeerTracker, fi *fleet.FleetIndex) swarmEntry {
	c, d, p, sk, t := tracker.StatusSummary(sha256)
	var ret string
	if seed.Retention > 0 {
		days := int(seed.Retention.Hours() / 24)
		if days > 0 {
			ret = fmt.Sprintf("%dd", days)
		} else {
			ret = seed.Retention.String()
		}
	}
	source := "seed"
	if seed.SeederID != "" && fi != nil {
		if es := fi.GetEnvoy(seed.SeederID); es != nil {
			source = es.Hostname
		}
	}
	e := swarmEntry{
		SHA256:      sha256,
		Label:       seed.Label,
		Size:        int64(seed.Size),
		Source:      source,
		Target:      seed.Target,
		Retention:   ret,
		Complete:    c,
		Downloading: d,
		Pending:     p,
		Skipped:     sk,
		Total:       t,
		CreatedAt:   seed.DistributedAt.Format("2006-01-02T15:04:05Z"),
	}
	if seed.Retired {
		e.Retired = true
		e.RetiredAt = seed.RetiredAt.Format("2006-01-02T15:04:05Z")
	}
	return e
}

// Delete removes a seed from the tracker and optionally from the seed directory.
func (h *SwarmHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	hash := r.PathValue("sha256")
	if !swarm.IsValidHash(hash) {
		// Resolve prefix: try PeerTracker first, then ManifestStore.
		if full, ok := h.tracker.ResolveSeedPrefix(hash); ok {
			hash = full
		} else if h.manifestStore != nil {
			if full, ok := h.manifestStore.ResolvePrefix(hash); ok {
				hash = full
			} else {
				writeJSONError(w, http.StatusNotFound, "seed not found")
				return
			}
		} else {
			writeJSONError(w, http.StatusNotFound, "seed not found")
			return
		}
	}

	// Get seed info BEFORE retiring (RetireSeed doesn't delete, but we
	// need the label to clean up files from the server's seed directory).
	if h.seedDir != "" {
		if seed, ok := h.tracker.GetSeed(hash); ok && seed.Label != "" {
			os.Remove(filepath.Join(h.seedDir, seed.Label))
			os.Remove(filepath.Join(h.seedDir, seed.Label+".swarm"))
		}
	}

	// Retire in the tracker (no-op if not present) and record revocation
	// in the manifest CRDT for gossip propagation. The revocation entry
	// is included in check-in responses so envoys evict orphaned blobs
	// even when the seed is no longer in the tracker.
	h.tracker.RetireSeed(hash)
	if h.manifestStore != nil {
		h.manifestStore.Revoke(hash)
	}

	writeJSON(w, http.StatusOK, map[string]any{"deleted": hash})
}

// Status returns per-envoy distribution progress for a blob.
func (h *SwarmHandler) Status(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "swarm not enabled")
		return
	}

	hash := r.PathValue("sha256")
	if !swarm.IsValidHash(hash) {
		if full, ok := h.tracker.ResolveSeedPrefix(hash); ok {
			hash = full
		} else {
			if swarm.IsValidHashPrefix(hash) {
				writeJSONError(w, http.StatusNotFound, "no seed matches prefix")
			} else {
				writeJSONError(w, http.StatusBadRequest, "invalid sha256")
			}
			return
		}
	}

	var totalChunks int
	var label string
	if seed, ok := h.tracker.GetSeed(hash); ok {
		totalChunks = chunksForSize(int64(seed.Size))
		label = seed.Label
	}

	seederID := ""
	if seed, ok := h.tracker.GetSeed(hash); ok {
		seederID = seed.SeederID
	}
	statuses := h.tracker.Status(hash, totalChunks, seederID)

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

// Match returns the count of non-revoked envoys matching a target spec.
// Used by the CLI to warn when a distribute targets zero envoys.
func (h *SwarmHandler) Match(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		target = "*"
	}

	count := 0
	if h.fleetIndex != nil {
		for _, envoy := range h.fleetIndex.AllEnvoys() {
			if envoy.Revoked {
				continue
			}
			if swarm.MatchTarget(envoy.Hostname, envoy.Traits, target) {
				count++
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"target": target, "count": count})
}

// copyFile copies src to dst as a fallback when os.Rename fails (cross-device).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}
