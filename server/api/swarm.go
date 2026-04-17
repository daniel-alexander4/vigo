// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
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
	seedDir        string
	tracker        *swarm.PeerTracker
	fleetIndex     *fleet.FleetIndex
	forcePusher    SwarmForcePusher
	manifestStore  *swarm.ManifestStore
	queries        *db.Queries
}

// NewSwarmHandler creates a new swarm handler.
func NewSwarmHandler(seedDir string, tracker *swarm.PeerTracker, fleetIndex *fleet.FleetIndex, manifest *swarm.ManifestStore, queries *db.Queries) *SwarmHandler {
	return &SwarmHandler{
		seedDir:       seedDir,
		tracker:       tracker,
		fleetIndex:    fleetIndex,
		manifestStore: manifest,
		queries:       queries,
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
	mux.HandleFunc("DELETE /api/v1/swarm/{sha256}", h.Revoke)
	mux.HandleFunc("GET /api/v1/swarm/broken", h.BrokenSeeds)
	mux.HandleFunc("GET /api/v1/swarm/match", h.Match)
	mux.HandleFunc("GET /api/v1/swarm/seeders", h.ListSeeders)
	mux.HandleFunc("POST /api/v1/swarm/seeders", h.AddSeeder)
	mux.HandleFunc("DELETE /api/v1/swarm/seeders/{id}", h.RemoveSeeder)
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
	buf := make([]byte, 4<<20) // 4 MiB buffer — reduces syscalls for multi-GiB uploads
	size, err := io.CopyBuffer(mw, r.Body, buf)
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

	// Register the seed in the tracker immediately so that
	// "vigocli swarm status" works without waiting for the next check-in.
	// The agent's trait collector will re-register on the next cycle
	// (idempotent — updates mutable fields only).
	if h.tracker != nil {
		retDur := parseSwarmRetention(retention)
		h.tracker.RegisterSeed(hash, swarm.SeedState{
			Label:         label,
			Size:          uint64(size),
			Target:        target,
			Retention:     retDur,
			SeederID:      "server",
			DistributedAt: time.Now(),
		})
	}
	if h.manifestStore != nil {
		h.manifestStore.RecordSeed(hash, label, target, retention, "server", uint64(size))
	}

	// Kick streaming agents so they pick up the manifest entry.
	if h.forcePusher != nil {
		h.forcePusher.ForcePush("")
	}

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
	Cached      int    `json:"cached"`
	Downloading int    `json:"downloading"`
	Pending     int    `json:"pending"`
	Skipped     int    `json:"skipped"`
	Total       int    `json:"total"`
	CreatedAt   string `json:"created_at,omitempty"`
	Revoked     bool   `json:"revoked,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
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

	// Include revoked seeds if requested.
	if r.URL.Query().Get("revoked") == "true" {
		for sha256, seed := range h.tracker.ListRevokedSeeds() {
			entries = append(entries, seedToEntry(sha256, seed, h.tracker, h.fleetIndex))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"blobs": entries})
}

func seedToEntry(sha256 string, seed swarm.SeedState, tracker *swarm.PeerTracker, fi *fleet.FleetIndex) swarmEntry {
	// StatusSummary returns an additional "source" count that the
	// list endpoint currently doesn't surface separately — the list
	// view rolls source into the "done" bucket at render time. Kept
	// in the return signature so it's available for future callers.
	c, d, p, sk, _, ca, t := tracker.StatusSummary(sha256)
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
		Cached:      ca,
		Downloading: d,
		Pending:     p,
		Skipped:     sk,
		Total:       t,
		CreatedAt:   seed.DistributedAt.Format("2006-01-02T15:04:05Z"),
	}
	if seed.Revoked {
		e.Revoked = true
		e.RevokedAt = seed.RevokedAt.Format("2006-01-02T15:04:05Z")
	}
	return e
}

// Revoke marks a seed as revoked: tracker entry transitions to revoked state,
// manifest CRDT records the revocation for gossip propagation, and the
// server's local seed file is removed so the trait collector stops re-reporting
// it. Bound to HTTP DELETE /api/v1/swarm/{sha256}.
func (h *SwarmHandler) Revoke(w http.ResponseWriter, r *http.Request) {
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

	// Check if already revoked before doing work.
	alreadyRevoked := false
	if seed, ok := h.tracker.GetSeed(hash); ok && seed.Revoked {
		alreadyRevoked = true
	} else if h.manifestStore != nil && h.manifestStore.IsRevoked(hash) {
		alreadyRevoked = true
	}

	if !alreadyRevoked {
		// Get seed info before revoking — we need the label to clean up
		// the source file from the server's seed directory.
		if h.seedDir != "" {
			if seed, ok := h.tracker.GetSeed(hash); ok && seed.Label != "" {
				os.Remove(filepath.Join(h.seedDir, seed.Label))
				os.Remove(filepath.Join(h.seedDir, seed.Label+".swarm"))
			}
		}

		// Revoke in the tracker (no-op if not present) and record revocation
		// in the manifest CRDT for gossip propagation. Envoys evict on the
		// next manifest merge (check-in or peer multicast).
		h.tracker.RevokeSeed(hash)
		if h.manifestStore != nil {
			h.manifestStore.Revoke(hash)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"revoked":         hash,
		"already_revoked": alreadyRevoked,
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

	var seederID, target string
	if seed, ok := h.tracker.GetSeed(hash); ok {
		seederID = seed.SeederID
		target = seed.Target
	}
	statuses := h.tracker.Status(hash, totalChunks, seederID)

	// Resolve envoy IDs to hostnames.
	seen := make(map[string]bool, len(statuses))
	if h.fleetIndex != nil {
		for i := range statuses {
			if state := h.fleetIndex.GetEnvoy(statuses[i].Hostname); state != nil {
				seen[state.Hostname] = true
				statuses[i].Hostname = state.Hostname
			} else {
				seen[statuses[i].Hostname] = true
			}
		}
	}

	// Compute the blob-level revocation status once so the pending fan-out
	// and the summary logic can both consult it.
	blobStatus := "active"
	if h.manifestStore != nil && h.manifestStore.IsRevoked(hash) {
		blobStatus = "revoked"
	} else if seed, ok := h.tracker.GetSeed(hash); ok && seed.Revoked {
		blobStatus = "revoked"
	}

	// Add envoys that should eventually have the blob but haven't
	// reported chunks yet as "pending". Skipped entirely when the
	// seed is revoked — a "pending" row represents an envoy that
	// hasn't even started downloading, which is irrelevant once the
	// blob has been revoked.
	//
	// Two fan-out modes depending on label:
	//
	//   1. Claim-only labels (vigorepo): authoritative source is the
	//      vigorepo ClaimTable. Only the envoys selected as mirrors
	//      show up. Envoys excluded by the sidecar's exclude_envoys /
	//      include_envoys / when filters, or simply not picked
	//      within max_mirrors, never appear — showing them as
	//      "pending" would be a lie because they will never receive
	//      the blob.
	//
	//   2. Standard labels: target pattern fan-out against the full
	//      fleet, matching the existing pre-vigorepo behavior.
	if blobStatus != "revoked" && h.fleetIndex != nil {
		if swarm.IsClaimOnlyLabel(label) {
			// Phase 8 (0.23.0): the server no longer tracks which
			// envoys hold vigorepo mirrors. Selection is entirely
			// LAN/cross-subnet peer-to-peer. The UI's per-blob
			// "pending envoys" fan-out is skipped for claim-only
			// labels; operators view vigorepo state via the
			// receiver's `vigo repo list` command or the
			// `vigorepo.sources[]` trait payload.
		} else if target != "" {
			for _, envoy := range h.fleetIndex.AllEnvoys() {
				if envoy.Revoked || seen[envoy.Hostname] {
					continue
				}
				if swarm.MatchTarget(envoy.Hostname, envoy.Traits, target) {
					seen[envoy.Hostname] = true
					statuses = append(statuses, swarm.EnvoySwarmStatus{
						Hostname:   envoy.Hostname,
						Status:     "pending",
						ChunksNeed: totalChunks,
					})
				}
			}
		}
	}

	// Compute summary counts. revoking/revoked are meaningful only when
	// the blob is revoked; source/complete/downloading/pending/skipped
	// are meaningful only when the blob is active. The CLI and UI choose
	// which set to display based on blobStatus.
	var source, complete, cached, downloading, pending, skipped, revoking, revoked int
	for _, s := range statuses {
		switch s.Status {
		case "source":
			source++
		case "complete":
			complete++
		case "cached":
			cached++
		case "downloading":
			downloading++
		case "pending":
			pending++
		case "skipped":
			skipped++
		case "revoking":
			revoking++
		case "revoked":
			revoked++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sha256":      hash,
		"label":       label,
		"status":      blobStatus,
		"chunk_count": totalChunks,
		"summary": map[string]int{
			"source":      source,
			"complete":    complete,
			"cached":      cached,
			"downloading": downloading,
			"pending":     pending,
			"skipped":     skipped,
			"revoking":    revoking,
			"revoked":     revoked,
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

// ListSeeders returns all seed allowlist patterns.
func (h *SwarmHandler) ListSeeders(w http.ResponseWriter, r *http.Request) {
	entries, err := h.queries.ListSwarmSeedAllowEntries(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "listing seeders: "+err.Error())
		return
	}
	type seederEntry struct {
		ID        string `json:"id"`
		Pattern   string `json:"pattern"`
		CreatedAt string `json:"created_at"`
		CreatedBy string `json:"created_by"`
	}
	out := make([]seederEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, seederEntry{
			ID:        e.ID,
			Pattern:   e.Pattern,
			CreatedAt: e.CreatedAt.Format(time.RFC3339),
			CreatedBy: e.CreatedBy,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// AddSeeder adds a hostname pattern to the seed allowlist.
func (h *SwarmHandler) AddSeeder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pattern string `json:"pattern"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Pattern == "" {
		writeJSONError(w, http.StatusBadRequest, "pattern is required")
		return
	}
	entry, err := h.queries.CreateSwarmSeedAllowEntry(r.Context(), db.CreateSwarmSeedAllowEntryParams{
		Pattern:   req.Pattern,
		CreatedBy: "admin",
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			writeJSONError(w, http.StatusConflict, "pattern already exists")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "creating entry: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":      entry.ID,
		"pattern": entry.Pattern,
	})
}

// RemoveSeeder removes a seed allowlist entry by ID.
func (h *SwarmHandler) RemoveSeeder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.queries.DeleteSwarmSeedAllowEntry(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "deleting entry: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
