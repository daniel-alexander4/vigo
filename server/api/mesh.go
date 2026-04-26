// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm/cryptsyncmesh"
	"git.alexander4.io/vigo/server/swarm/filesyncmesh"
)

// MeshHandler serves the fleet-wide filesync + cryptsync mesh
// observability endpoints. Both aggregators live alongside because
// the UI renders them on a single `/mesh` page with subsystem tabs.
type MeshHandler struct {
	fleetIndex *fleet.FleetIndex
	sync       *filesyncmesh.Aggregator
	crypt      *cryptsyncmesh.Aggregator
	authMethod string
}

func NewMeshHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries, authMethod string) *MeshHandler {
	return &MeshHandler{
		fleetIndex: fleetIndex,
		sync:       filesyncmesh.NewAggregator(queries),
		crypt:      cryptsyncmesh.NewAggregator(queries),
		authMethod: authMethod,
	}
}

// RegisterRoutes mounts mesh endpoints behind the admin RBAC
// middleware — fleet-wide per-user sync and crypt state is only
// visible to admins.
func (h *MeshHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	mux.Handle("GET /api/v1/swarm/filesync/fleet", adminMW(http.HandlerFunc(h.handleFilesyncFleet)))
	mux.Handle("GET /api/v1/swarm/cryptsync/fleet", adminMW(http.HandlerFunc(h.handleCryptsyncFleet)))
	mux.Handle("GET /api/v1/swarm/filesync/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleFilesyncDrilldown)))
	mux.Handle("GET /api/v1/swarm/cryptsync/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleCryptsyncDrilldown)))
}

// SyncSnapshot returns the cached filesync fleet view. Used by the
// UI handler directly (no HTTP round-trip).
func (h *MeshHandler) SyncSnapshot(ctx context.Context) (*filesyncmesh.Fleet, error) {
	return h.sync.Snapshot(ctx, h.envoyInputsSync())
}

// CryptSnapshot returns the cached cryptsync fleet view.
func (h *MeshHandler) CryptSnapshot(ctx context.Context) (*cryptsyncmesh.Fleet, error) {
	return h.crypt.Snapshot(ctx, h.envoyInputsCrypt())
}

// EnvoyFilesyncFiles returns per-envoy filesync files for the Swarm Load
// page. Passthrough to the aggregator so the UI handler can consume
// it through the MeshSource interface without importing filesyncmesh.
func (h *MeshHandler) EnvoyFilesyncFiles(ctx context.Context, envoyID string) []filesyncmesh.EnvoyFile {
	return h.sync.FilesOnEnvoy(ctx, envoyID)
}

// EnvoyCryptsyncFiles returns per-envoy cryptsync files for the Swarm Load
// page. Passthrough to the aggregator.
func (h *MeshHandler) EnvoyCryptsyncFiles(ctx context.Context, envoyID string) []cryptsyncmesh.EnvoyFile {
	return h.crypt.FilesOnEnvoy(ctx, envoyID)
}

func (h *MeshHandler) envoyInputsSync() []filesyncmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]filesyncmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, filesyncmesh.EnvoyInput{
			ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked,
		})
	}
	return out
}

func (h *MeshHandler) envoyInputsCrypt() []cryptsyncmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]cryptsyncmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, cryptsyncmesh.EnvoyInput{
			ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked,
		})
	}
	return out
}

func (h *MeshHandler) handleFilesyncFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.SyncSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "filesync fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("filesync fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleCryptsyncFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.CryptSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cryptsync fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("cryptsync fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleFilesyncDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeJSONError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.sync.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "filesync drilldown failed")
		return
	}
	if files == nil {
		files = []filesyncmesh.FileRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Files []filesyncmesh.FileRow `json:"files"`
	}{Files: files}); err != nil {
		slog.Debug("filesync drilldown response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleCryptsyncDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeJSONError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.crypt.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cryptsync drilldown failed")
		return
	}
	if files == nil {
		files = []cryptsyncmesh.FileRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Files []cryptsyncmesh.FileRow `json:"files"`
	}{Files: files}); err != nil {
		slog.Debug("cryptsync drilldown response encode failed", "error", err)
	}
}
