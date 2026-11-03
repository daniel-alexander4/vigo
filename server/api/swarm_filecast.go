// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm"
)

// Filecast is the content label for administrator-pushed filecasts that ride
// the swarm substrate. This handler wires the admin-facing REST surface
// (`/api/v1/swarm/filecast/*`); the substrate itself — tracker, manifest CRDT,
// peer transfer — still lives in `server/swarm/` because it's shared
// with gitback and (read-side) longdrawer/lockbox.

// LodeForcePusher triggers a force-push to streaming agents so they
// send a fresh CheckIn and receive pending payload assignments.
type LodeForcePusher interface {
	ForcePush(envoyID string) int
}

// LodeHandler handles filecast distribution REST endpoints.
type LodeHandler struct {
	seedDir       string
	tracker       *swarm.PeerTracker
	fleetIndex    *fleet.FleetIndex
	forcePusher   LodeForcePusher
	manifestStore *swarm.ManifestStore
	queries       *db.Queries

	swarmCfgMu  sync.RWMutex
	swarmCfg    config.SwarmSection
	auditWriter auditRecorder
}

// NewLodeHandler creates a new payload handler.
func NewLodeHandler(seedDir string, tracker *swarm.PeerTracker, fleetIndex *fleet.FleetIndex, manifest *swarm.ManifestStore, queries *db.Queries) *LodeHandler {
	return &LodeHandler{
		seedDir:       seedDir,
		tracker:       tracker,
		fleetIndex:    fleetIndex,
		manifestStore: manifest,
		queries:       queries,
	}
}

// SetForcePusher wires the stream manager's force push capability so that
// distribute triggers a fresh check-in from streaming agents.
func (h *LodeHandler) SetForcePusher(fp LodeForcePusher) {
	h.forcePusher = fp
}

// SetSwarmConfig captures the current swarm config so the Distribute
// handler can gate-check `swarm.enabled` and `swarm.filecast.enabled`
// before accepting an upload. Called from main.go at startup and from
// the config-publish hook on every reload.
func (h *LodeHandler) SetSwarmConfig(cfg config.SwarmSection) {
	h.swarmCfgMu.Lock()
	h.swarmCfg = cfg
	h.swarmCfgMu.Unlock()
}

// SetAuditWriter wires the tamper-evident audit-chain writer so denial
// events from gated admin operations land in the same chain as every
// other admin action.
func (h *LodeHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// payloadGate reports whether `vigocli swarm filecast distribute` should
// be allowed right now. Returns (allowed, reason) — when false, reason
// is the operator-facing explanation surfaced in the 403 body and the
// audit-chain summary. Refuses when neither layer would let any envoy
// receive a payload — i.e. swarm.enabled is empty (substrate off
// fleet-wide) or swarm.filecast.enabled is empty (subsystem off
// fleet-wide). Per-host gating is enforced agent-side; this check is
// just the floor.
func (h *LodeHandler) payloadGate() (bool, string) {
	h.swarmCfgMu.RLock()
	defer h.swarmCfgMu.RUnlock()
	if len(h.swarmCfg.Enabled) == 0 {
		return false, "swarm is disabled fleet-wide (swarm.enabled: [])"
	}
	if len(h.swarmCfg.Filecast.Enabled) == 0 {
		return false, "filecast subsystem is disabled (swarm.filecast.enabled: [])"
	}
	return true, ""
}

func (h *LodeHandler) recordPayloadDenial(ctx context.Context, actor, reason string) {
	payload := fmt.Sprintf(`{"endpoint":"/api/v1/swarm/filecast/distribute","reason":%q}`, reason)
	recordAudit(ctx, h.auditWriter, "swarm.filecast.denied", actor, "api", reason, payload)
}

// RegisterRoutes registers payload endpoints. Distribute and revoke
// are admin-only — they place and remove blobs on the swarm seed
// directory and trigger force-pushes to streaming agents. Read
// endpoints (list / status / broken / match) gate on fleet.read;
// they aggregate per-envoy distribution state across the swarm.
func (h *LodeHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("POST /api/v1/swarm/filecast/distribute", admin(http.HandlerFunc(h.Distribute)))
	mux.Handle("GET /api/v1/swarm/filecast/list", read(http.HandlerFunc(h.List)))
	mux.Handle("GET /api/v1/swarm/filecast/status/{sha256}", read(http.HandlerFunc(h.Status)))
	mux.Handle("DELETE /api/v1/swarm/filecast/{sha256}", admin(http.HandlerFunc(h.Revoke)))
	mux.Handle("POST /api/v1/swarm/filecast/purge", admin(http.HandlerFunc(h.Purge)))
	mux.Handle("GET /api/v1/swarm/filecast/broken", read(http.HandlerFunc(h.BrokenSeeds)))
	mux.Handle("GET /api/v1/swarm/filecast/match", read(http.HandlerFunc(h.Match)))
}

// Distribute accepts a file upload and places it in the server's seed directory
// with a .swarm manifest. The server's own agent discovers it on the next
// check-in via the swarm_seeds trait collector and auto-distributes to targets.
func (h *LodeHandler) Distribute(w http.ResponseWriter, r *http.Request) {
	if h.seedDir == "" {
		writeError(w, http.StatusServiceUnavailable, "swarm substrate not enabled — payload requires swarm")
		return
	}

	// Substrate operability gate. When the operator has flipped
	// `swarm.enabled: false` or `swarm.filecast.enabled: false` in
	// server.yaml and republished, refuse new admin pushes — existing
	// payloads keep flowing on agents that hold them, but no new
	// admin-initiated work lands. Audit the denial so an operator can
	// see who hit the gate.
	if allowed, reason := h.payloadGate(); !allowed {
		actor := "anonymous"
		if u := auth.UserFromContext(r.Context()); u != nil {
			actor = u.Username
		}
		h.recordPayloadDenial(r.Context(), actor, reason)
		writeError(w, http.StatusForbidden, reason)
		return
	}

	label := r.Header.Get("X-Filecast-Label")
	target := r.Header.Get("X-Filecast-Target")
	retention := r.Header.Get("X-Filecast-Retention")

	if label == "" {
		writeError(w, http.StatusBadRequest, "X-Payload-Label header required")
		return
	}
	if target == "" {
		target = "*"
	}
	if retention == "" {
		// Per swarm.filecast.default_retention if set, else compile-time default.
		h.swarmCfgMu.RLock()
		retention = h.swarmCfg.Filecast.DefaultRetention
		h.swarmCfgMu.RUnlock()
		if retention == "" {
			retention = "7d"
		}
	}

	// Per-distribute size cap (swarm.filecast.max_size_bytes). Fast-path
	// reject via Content-Length when set; the streaming copy below also
	// checks against the cap as it writes so a chunked upload that lies
	// in Content-Length still fails closed at the cap.
	h.swarmCfgMu.RLock()
	maxSize := h.swarmCfg.Filecast.MaxSizeBytes
	h.swarmCfgMu.RUnlock()
	if maxSize > 0 && r.ContentLength > maxSize {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("payload size %d exceeds swarm.filecast.max_size_bytes (%d)", r.ContentLength, maxSize))
		return
	}

	// Sanitize label — no path separators allowed.
	label = filepath.Base(label)

	// Write manifest FIRST to avoid race with trait collector seeing
	// the file before the manifest exists (would report as broken seed).
	// The on-disk format remains `.swarm` — that's the substrate's sidecar
	// extension, not a content-type label.
	manifestPath := filepath.Join(h.seedDir, label+".swarm")
	manifest := fmt.Sprintf(`{"target":%q,"retention":%q}`, target, retention)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		slog.Error("writing manifest", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Stream file body to temp file while computing SHA256.
	tmp, err := os.CreateTemp(h.seedDir, ".upload-*")
	if err != nil {
		os.Remove(manifestPath)
		slog.Error("creating temp file", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
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
	// If a per-distribute cap is set, wrap r.Body in an io.LimitReader
	// so a chunked upload (Content-Length unset / 0) that runs past the
	// cap fails closed instead of consuming arbitrary disk + bandwidth.
	bodyReader := r.Body
	if maxSize > 0 {
		bodyReader = http.MaxBytesReader(w, bodyReader, maxSize)
	}
	size, err := io.CopyBuffer(mw, bodyReader, buf)
	if err != nil {
		os.Remove(manifestPath)
		// http.MaxBytesError is what MaxBytesReader returns on overflow.
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("payload exceeded swarm.filecast.max_size_bytes (%d)", maxSize))
			return
		}
		slog.Error("writing file", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(manifestPath)
		slog.Error("closing temp file", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	hash := hex.EncodeToString(hasher.Sum(nil))

	// Move to final path.
	destPath := filepath.Join(h.seedDir, label)
	if err := os.Rename(tmpPath, destPath); err != nil {
		// Cross-device: copy + delete.
		if copyErr := copyFile(tmpPath, destPath); copyErr != nil {
			os.Remove(manifestPath)
			slog.Error("moving file", "error", copyErr)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	slog.Info("payload seed placed",
		"sha256", hash[:12],
		"label", label,
		"target", target,
		"retention", retention,
		"size", size,
	)

	// Register the seed in the tracker immediately so that
	// "vigocli payload status" works without waiting for the next check-in.
	// The agent's trait collector will re-register on the next cycle
	// (idempotent — updates mutable fields only).
	if h.tracker != nil {
		retDur := parseLodeRetention(retention)
		h.tracker.RegisterSeed(hash, swarm.SeedState{
			Label:         label,
			Size:          uint64(size),
			Target:        target,
			Retention:     retDur,
			SeederID:      "server",
			DistributedAt: time.Now(),
		})
		// For non-revoked re-distributes, RegisterSeed doesn't update
		// DistributedAt (to avoid the trait re-registration race).
		// Touch it explicitly so cache-hit detection works.
		h.tracker.TouchDistributedAt(hash)
	}
	if h.manifestStore != nil {
		h.manifestStore.RecordSeed(hash, label, target, retention, "server", uint64(size))
	}

	// Kick streaming agents so they pick up the manifest entry.
	if h.forcePusher != nil {
		h.forcePusher.ForcePush("")
	}

	recordAudit(r.Context(), h.auditWriter, "swarm.filecast.distribute", actorFromRequest(r), "rest",
		fmt.Sprintf("distributed filecast %s (%s, %d bytes) to %s", hash[:12], label, size, target), "")

	writeJSON(w, r, http.StatusCreated, map[string]any{
		"sha256": hash,
		"label":  label,
		"size":   size,
	})
}

// lodeEntry is a unified payload entry for the list API.
type lodeEntry struct {
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

// List returns all filecasts with status summaries.
func (h *LodeHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeError(w, http.StatusServiceUnavailable, "swarm substrate not enabled — payload requires swarm")
		return
	}

	var entries []lodeEntry

	// Active seeds.
	for sha256, seed := range h.tracker.ListSeeds() {
		entries = append(entries, seedToLodeEntry(sha256, seed, h.tracker, h.fleetIndex))
	}

	// Include revoked seeds if requested.
	if r.URL.Query().Get("revoked") == "true" {
		for sha256, seed := range h.tracker.ListRevokedSeeds() {
			entries = append(entries, seedToLodeEntry(sha256, seed, h.tracker, h.fleetIndex))
		}
	}

	writeJSON(w, r, http.StatusOK, map[string]any{"payloads": entries})
}

func seedToLodeEntry(sha256 string, seed swarm.SeedState, tracker *swarm.PeerTracker, fi *fleet.FleetIndex) lodeEntry {
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
	e := lodeEntry{
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
// it. Bound to HTTP DELETE /api/v1/swarm/filecast/{sha256}.
func (h *LodeHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeError(w, http.StatusServiceUnavailable, "swarm substrate not enabled — payload requires swarm")
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
				writeError(w, http.StatusNotFound, "payload not found")
				return
			}
		} else {
			writeError(w, http.StatusNotFound, "payload not found")
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

	if !alreadyRevoked {
		recordAudit(r.Context(), h.auditWriter, "swarm.filecast.revoke", actorFromRequest(r), "rest",
			"revoked filecast "+hash[:12], "")
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"revoked":         hash,
		"already_revoked": alreadyRevoked,
	})
}

// Purge garbage-collects revoked filecast tombstones from the manifest.
// revoke is a soft-delete — the entry stays as a `revoked` CRDT tombstone
// so the blob can't silently resurrect — and the retention pruner only
// GCs them after the retention window. Purge lets an operator drop them
// now. Optional ?older_than=<dur> limits the purge to tombstones older
// than that window; default (0) purges every revoked entry. Re-poison
// safe: RecordSeedFromAgent rejects any SHA that isn't already an active
// server entry, so agents can't push a purged tombstone back.
func (h *LodeHandler) Purge(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeError(w, http.StatusServiceUnavailable, "swarm substrate not enabled — payload requires swarm")
		return
	}
	if h.manifestStore == nil {
		writeJSON(w, r, http.StatusOK, map[string]any{"purged": 0})
		return
	}

	var maxAge time.Duration
	if s := r.URL.Query().Get("older_than"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid older_than duration: "+err.Error())
			return
		}
		maxAge = d
	}

	pruned := h.manifestStore.PruneRevoked(maxAge)
	if pruned > 0 {
		recordAudit(r.Context(), h.auditWriter, "swarm.filecast.purge", actorFromRequest(r), "rest",
			fmt.Sprintf("purged %d revoked filecast(s)", pruned), "")
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"purged": pruned})
}

// Status returns per-envoy distribution progress for a payload.
func (h *LodeHandler) Status(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeError(w, http.StatusServiceUnavailable, "swarm substrate not enabled — payload requires swarm")
		return
	}

	hash := r.PathValue("sha256")
	if !swarm.IsValidHash(hash) {
		if full, ok := h.tracker.ResolveSeedPrefix(hash); ok {
			hash = full
		} else {
			if swarm.IsValidHashPrefix(hash) {
				writeError(w, http.StatusNotFound, "no payload matches prefix")
			} else {
				writeError(w, http.StatusBadRequest, "invalid sha256")
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

	// Compute the payload-level revocation status once so the pending fan-out
	// and the summary logic can both consult it.
	blobStatus := "active"
	if h.manifestStore != nil && h.manifestStore.IsRevoked(hash) {
		blobStatus = "revoked"
	} else if seed, ok := h.tracker.GetSeed(hash); ok && seed.Revoked {
		blobStatus = "revoked"
	}

	// Add envoys that should eventually have the payload but haven't
	// reported chunks yet as "pending". Skipped entirely when the
	// seed is revoked — a "pending" row represents an envoy that
	// hasn't even started downloading, which is irrelevant once the
	// payload has been revoked.
	//
	// Two fan-out modes depending on label:
	//
	//   1. Claim-only labels (gitback): authoritative source is the
	//      gitback ClaimTable. Only the envoys selected as mirrors
	//      show up. Envoys excluded by the sidecar's exclude_envoys /
	//      include_envoys / when filters, or simply not picked
	//      within max_mirrors, never appear — showing them as
	//      "pending" would be a lie because they will never receive
	//      the payload.
	//
	//   2. Standard labels: target pattern fan-out against the full
	//      fleet, matching the existing pre-gitback behavior.
	if blobStatus != "revoked" && h.fleetIndex != nil {
		if swarm.IsClaimOnlyLabel(label) {
			// Phase 8 (0.23.0): the server no longer tracks which
			// envoys hold gitback mirrors. Selection is entirely
			// LAN/cross-subnet peer-to-peer. The UI's per-payload
			// "pending envoys" fan-out is skipped for claim-only
			// labels; operators view gitback state via the
			// receiver's `vigo swarm gitback list` command or the
			// `gitback.sources[]` trait payload.
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
	// the payload is revoked; source/complete/downloading/pending/skipped
	// are meaningful only when the payload is active. The CLI and UI choose
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

	writeJSON(w, r, http.StatusOK, map[string]any{
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
			"total":       len(statuses) - source,
		},
		"envoys": statuses,
	})
}

// chunksForSize returns the number of 1 MiB chunks for a payload.
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

// parseLodeRetention parses "7d" or "168h" style duration strings.
func parseLodeRetention(s string) time.Duration {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	d, _ := time.ParseDuration(s)
	return d
}

// BrokenSeeds returns broken seed manifests across all envoys.
func (h *LodeHandler) BrokenSeeds(w http.ResponseWriter, r *http.Request) {
	if h.tracker == nil {
		writeError(w, http.StatusServiceUnavailable, "swarm substrate not enabled — payload requires swarm")
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

	writeJSON(w, r, http.StatusOK, map[string]any{"envoys": envoys})
}

// Match returns the count of non-revoked envoys matching a target spec.
// Used by the CLI to warn when a distribute targets zero envoys.
func (h *LodeHandler) Match(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, r, http.StatusOK, map[string]any{"target": target, "count": count})
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
