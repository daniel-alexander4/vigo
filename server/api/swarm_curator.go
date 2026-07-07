// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
)

// CuratorHandler serves the curator (ADR-024) REST surface — the
// server-mediated read path that lets every envoy, swarm-enabled or not,
// resolve `<name>:<tag>` to a blob and (as a fallback) fetch the bytes, plus
// the `PermFleetRead` operator views and the admin `block`/`unblock` moderation
// backstop. Aggregation lives in `curatormesh`; the agent verifies signed
// entries end-to-end, so the server is purely a relay (it doesn't validate
// signatures — see the curatormesh package doc).
//
// The agent-facing endpoints (`resolve`, `blob`) are public-by-construction —
// artifacts are fleet-readable (the publish side is what's gated, via the
// usercrate `curator:` flag), so there's nothing to authenticate beyond the
// TLS transport, exactly like `/api/v1/swarm/puddle/names`. The `recipients`
// field on a catalog entry is currently informational; per-recipient gating
// would pair with the post-v1 encrypted-blob feature.
type CuratorHandler struct {
	agg         *curatormesh.Aggregator
	queries     *db.Queries
	fleetIndex  *fleet.FleetIndex
	blobsDir    string // server's co-resident envoy's blob cache (bind-mounted)
	auditWriter auditRecorder
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *CuratorHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// NewCuratorHandler builds the handler. `blobsDir` is the directory the
// `blob` relay serves from — the swarm blob cache (`/var/lib/vigo/swarm/.blobs`),
// which the server container shares with its co-resident agent.
func NewCuratorHandler(agg *curatormesh.Aggregator, queries *db.Queries, fleetIndex *fleet.FleetIndex, blobsDir string) *CuratorHandler {
	return &CuratorHandler{agg: agg, queries: queries, fleetIndex: fleetIndex, blobsDir: blobsDir}
}

// RegisterRoutes mounts the curator endpoints. `resolve` + `blob` are public
// (added to server/auth/auth.go's public paths); the operator views are
// fleet.read; `block`/`unblock` are admin-only.
func (h *CuratorHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.HandleFunc("GET /api/v1/swarm/curator/resolve", h.handleResolve)
	mux.HandleFunc("GET /api/v1/swarm/curator/blob/{sha256}", h.handleBlob)
	mux.Handle("GET /api/v1/swarm/curator/list", read(http.HandlerFunc(h.handleList)))
	mux.Handle("GET /api/v1/swarm/curator/inspect/{artifact_id}", read(http.HandlerFunc(h.handleInspect)))
	mux.Handle("GET /api/v1/swarm/curator/versions/{artifact_id}", read(http.HandlerFunc(h.handleVersions)))
	mux.Handle("GET /api/v1/swarm/curator/fleet", read(http.HandlerFunc(h.handleFleet)))
	mux.Handle("POST /api/v1/swarm/curator/block/{artifact_id}", admin(http.HandlerFunc(h.handleBlock)))
	mux.Handle("POST /api/v1/swarm/curator/unblock/{artifact_id}", admin(http.HandlerFunc(h.handleUnblock)))
}

func (h *CuratorHandler) envoyInputs() []curatormesh.EnvoyInput {
	if h.fleetIndex == nil {
		return nil
	}
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]curatormesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, curatormesh.EnvoyInput{ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked})
	}
	return out
}

// snapshot returns the fleet catalog. Slice 4d wired a 30-second cache into
// curatormesh's `Aggregator.Snapshot`, so a burst of `resolve`+`blob` calls
// from a single `vigo swarm curator pull` flow only pays the per-envoy trait
// scan + JSON parse once. Block/unblock writes invalidate the cache directly
// so the moderation backstop takes effect immediately rather than waiting up
// to one TTL window.
func (h *CuratorHandler) snapshot(r *http.Request) (*curatormesh.Catalog, error) {
	return h.agg.Snapshot(r.Context(), h.envoyInputs())
}

// FleetSnapshot returns the cached fleet catalog plus the
// operator-block table. Passthrough for the UI handler (slice 5b) —
// the `/curator` page reads this via the [CuratorFleetSource]
// interface in `ui/swarm_curator.go` so the ui package doesn't import
// curatormesh + db directly.
func (h *CuratorHandler) FleetSnapshot(ctx context.Context) (*curatormesh.Catalog, []db.CuratorBlockedArtifact, error) {
	cat, err := h.agg.Snapshot(ctx, h.envoyInputs())
	if err != nil {
		return nil, nil, err
	}
	blocked, _ := h.queries.ListBlockedArtifacts(ctx)
	if blocked == nil {
		blocked = []db.CuratorBlockedArtifact{}
	}
	return cat, blocked, nil
}

func (h *CuratorHandler) isBlocked(r *http.Request, artifactID string) bool {
	n, err := h.queries.IsArtifactBlocked(r.Context(), artifactID)
	return err == nil && n > 0
}

// --- agent-facing (public) ------------------------------------------------

type resolveResponse struct {
	ArtifactID  string                   `json:"artifact_id"`
	Version     string                   `json:"version"`
	BlobSha     string                   `json:"blob_sha"`
	Size        uint64                   `json:"size"`
	SignedEntry curatormesh.CatalogEntry `json:"signed_entry"`
}

// handleResolve: GET /api/v1/swarm/curator/resolve?artifact_id=<hex>&tag=<t>|&version=<v>&os=<os>&arch=<arch>
// The agent turns `<puddle-name>/<name>` into the hex artifact_id client-side
// from its cached name map; the hex form always works.
func (h *CuratorHandler) handleResolve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	artifactID := strings.ToLower(strings.TrimSpace(q.Get("artifact_id")))
	if !isHex64(artifactID) {
		writeError(w, http.StatusBadRequest, "artifact_id must be 64 hex characters")
		return
	}
	tag := strings.TrimSpace(q.Get("tag"))
	version := strings.TrimSpace(q.Get("version"))
	if tag == "" && version == "" {
		writeError(w, http.StatusBadRequest, "one of tag= or version= is required")
		return
	}
	os_ := strings.TrimSpace(q.Get("os"))
	arch := strings.TrimSpace(q.Get("arch"))
	if os_ == "" || arch == "" {
		writeError(w, http.StatusBadRequest, "os= and arch= are required")
		return
	}
	if h.isBlocked(r, artifactID) {
		writeError(w, http.StatusNotFound, "artifact is blocked by the operator")
		return
	}
	cat, err := h.snapshot(r)
	if err != nil {
		slog.Error("building catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	e := cat.LookupByID(artifactID)
	if e == nil {
		writeError(w, http.StatusNotFound, "no such artifact")
		return
	}
	tov := tag
	if tov == "" {
		tov = version
	}
	v, ok := curatormesh.ResolveVersion(e, tov)
	if !ok {
		writeError(w, http.StatusNotFound, "no such tag or version for this artifact")
		return
	}
	p := curatormesh.ResolvePlatform(e, v, os_, arch)
	if p == nil {
		writeError(w, http.StatusNotFound, "artifact has no build for "+os_+"/"+arch+" at version "+v)
		return
	}
	writeJSON(w, r, http.StatusOK, resolveResponse{
		ArtifactID:  artifactID,
		Version:     v,
		BlobSha:     strings.ToLower(p.BlobSha),
		Size:        p.Size,
		SignedEntry: *e,
	})
}

// handleBlob: GET /api/v1/swarm/curator/blob/{sha256} — relay the artifact bytes
// from the server's co-resident envoy's blob cache. Scoped to curator artifact
// blobs only (a sha that no current catalog entry references — or only blocked
// ones — is refused, so this isn't a back door to relay filecast/lockbox/gitback
// bytes).
func (h *CuratorHandler) handleBlob(w http.ResponseWriter, r *http.Request) {
	sha := strings.ToLower(strings.TrimSpace(r.PathValue("sha256")))
	if !isHex64(sha) {
		writeError(w, http.StatusBadRequest, "sha256 must be 64 hex characters")
		return
	}
	cat, err := h.snapshot(r)
	if err != nil {
		slog.Error("building catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// The sha must be referenced by at least one non-blocked catalog entry.
	served := false
	for i := range cat.Artifacts {
		if h.entryReferencesBlob(&cat.Artifacts[i], sha) {
			aid, aerr := curatormesh.ArtifactID(cat.Artifacts[i])
			if aerr == nil && !h.isBlocked(r, aid) {
				served = true
				break
			}
		}
	}
	if !served {
		writeError(w, http.StatusNotFound, "no curator artifact references that blob (or it is blocked)")
		return
	}
	path := filepath.Join(h.blobsDir, sha)
	f, err := os.Open(path)
	if err != nil {
		// The bytes aren't on this server's co-resident envoy (yet). Swarm-
		// enabled envoys should fetch P2P; this relay is a fallback.
		writeError(w, http.StatusNotFound, "blob not held by the server")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		slog.Error("stat blob", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, sha, st.ModTime(), f)
}

func (h *CuratorHandler) entryReferencesBlob(e *curatormesh.CatalogEntry, sha string) bool {
	for _, av := range e.Body.Versions {
		for _, p := range av.Platforms {
			if strings.EqualFold(p.BlobSha, sha) {
				return true
			}
		}
	}
	return false
}

// --- operator views (fleet.read) ------------------------------------------

type listResponse struct {
	Catalog *curatormesh.Catalog        `json:"catalog"`
	Blocked []db.CuratorBlockedArtifact `json:"blocked"`
}

func (h *CuratorHandler) handleList(w http.ResponseWriter, r *http.Request) {
	cat, err := h.snapshot(r)
	if err != nil {
		slog.Error("building catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	blocked, _ := h.queries.ListBlockedArtifacts(r.Context())
	if blocked == nil {
		blocked = []db.CuratorBlockedArtifact{}
	}
	writeJSON(w, r, http.StatusOK, listResponse{Catalog: cat, Blocked: blocked})
}

type inspectResponse struct {
	ArtifactID string                    `json:"artifact_id"`
	Entry      *curatormesh.CatalogEntry `json:"entry"`
	Blocked    bool                      `json:"blocked"`
}

func (h *CuratorHandler) handleInspect(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("artifact_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "artifact_id must be 64 hex characters")
		return
	}
	cat, err := h.snapshot(r)
	if err != nil {
		slog.Error("building catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	e := cat.LookupByID(id)
	blocked := h.isBlocked(r, id)
	if e == nil && !blocked {
		writeError(w, http.StatusNotFound, "no such artifact")
		return
	}
	writeJSON(w, r, http.StatusOK, inspectResponse{ArtifactID: id, Entry: e, Blocked: blocked})
}

type versionsResponse struct {
	ArtifactID string                                 `json:"artifact_id"`
	Name       string                                 `json:"name"`
	Versions   map[string]curatormesh.ArtifactVersion `json:"versions"`
	Tags       map[string]string                      `json:"tags"`
}

func (h *CuratorHandler) handleVersions(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("artifact_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "artifact_id must be 64 hex characters")
		return
	}
	cat, err := h.snapshot(r)
	if err != nil {
		slog.Error("building catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	e := cat.LookupByID(id)
	if e == nil {
		writeError(w, http.StatusNotFound, "no such artifact")
		return
	}
	writeJSON(w, r, http.StatusOK, versionsResponse{
		ArtifactID: id,
		Name:       e.Body.Name,
		Versions:   e.Body.Versions,
		Tags:       e.Body.Tags,
	})
}

type fleetResponse struct {
	GeneratedAt   any      `json:"generated_at"`
	Participants  []string `json:"participants"`
	ArtifactCount int      `json:"artifact_count"`
}

func (h *CuratorHandler) handleFleet(w http.ResponseWriter, r *http.Request) {
	cat, err := h.snapshot(r)
	if err != nil {
		slog.Error("building catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	parts := cat.Participants
	if parts == nil {
		parts = []string{}
	}
	writeJSON(w, r, http.StatusOK, fleetResponse{
		GeneratedAt:   cat.GeneratedAt,
		Participants:  parts,
		ArtifactCount: len(cat.Artifacts),
	})
}

// --- admin moderation -----------------------------------------------------

func (h *CuratorHandler) handleBlock(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("artifact_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "artifact_id must be 64 hex characters")
		return
	}
	actor := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		actor = u.Username
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if err := h.queries.BlockArtifact(r.Context(), db.BlockArtifactParams{
		ArtifactID: id,
		BlockedBy:  actor,
		Reason:     reason,
	}); err != nil {
		slog.Error("blocking artifact", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// `isBlocked` is consulted on every resolve/blob; the catalog snapshot's
	// cache doesn't gate that, but invalidating here keeps any rendered "is
	// this blocked?" column on /list / /inspect synchronously consistent with
	// the block we just wrote.
	h.agg.InvalidateCache()
	recordAudit(r.Context(), h.auditWriter, "swarm.curator.block", actor, "rest",
		"blocked curator artifact "+id+" (reason: "+reason+")", "")
	writeJSON(w, r, http.StatusOK, map[string]any{"ok": true, "artifact_id": id, "blocked": true})
}

func (h *CuratorHandler) handleUnblock(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("artifact_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "artifact_id must be 64 hex characters")
		return
	}
	if err := h.queries.UnblockArtifact(r.Context(), id); err != nil {
		slog.Error("unblocking artifact", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.agg.InvalidateCache()
	recordAudit(r.Context(), h.auditWriter, "swarm.curator.unblock", actorFromRequest(r), "rest",
		"unblocked curator artifact "+id, "")
	writeJSON(w, r, http.StatusOK, map[string]any{"ok": true, "artifact_id": id, "blocked": false})
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
