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
	"git.alexander4.io/vigo/server/swarm/longdrawermesh"
)

// MeshHandler serves the fleet-wide longdrawer + cryptsync mesh
// observability endpoints. Both aggregators live alongside because
// the UI renders them on a single `/mesh` page with subsystem tabs.
type MeshHandler struct {
	fleetIndex *fleet.FleetIndex
	sync       *longdrawermesh.Aggregator
	crypt      *cryptsyncmesh.Aggregator
	authMethod string
}

func NewMeshHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries, authMethod string) *MeshHandler {
	return &MeshHandler{
		fleetIndex: fleetIndex,
		sync:       longdrawermesh.NewAggregator(queries),
		crypt:      cryptsyncmesh.NewAggregator(queries),
		authMethod: authMethod,
	}
}

// RegisterRoutes mounts mesh endpoints behind the admin RBAC
// middleware — fleet-wide per-user sync and crypt state is only
// visible to admins.
func (h *MeshHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	mux.Handle("GET /api/v1/swarm/longdrawer/fleet", adminMW(http.HandlerFunc(h.handleLongdrawerFleet)))
	mux.Handle("GET /api/v1/swarm/cryptsync/fleet", adminMW(http.HandlerFunc(h.handleCryptsyncFleet)))
	mux.Handle("GET /api/v1/swarm/longdrawer/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleLongdrawerDrilldown)))
	mux.Handle("GET /api/v1/swarm/cryptsync/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleCryptsyncDrilldown)))
}

// SyncSnapshot returns the cached longdrawer fleet view. Used by the
// UI handler directly (no HTTP round-trip).
func (h *MeshHandler) SyncSnapshot(ctx context.Context) (*longdrawermesh.Fleet, error) {
	return h.sync.Snapshot(ctx, h.envoyInputsSync())
}

// CryptSnapshot returns the cached cryptsync fleet view.
func (h *MeshHandler) CryptSnapshot(ctx context.Context) (*cryptsyncmesh.Fleet, error) {
	return h.crypt.Snapshot(ctx, h.envoyInputsCrypt())
}

// EnvoyLongdrawerFiles returns per-envoy longdrawer files for the Swarm Load
// page. Passthrough to the aggregator so the UI handler can consume
// it through the MeshSource interface without importing longdrawermesh.
func (h *MeshHandler) EnvoyLongdrawerFiles(ctx context.Context, envoyID string) []longdrawermesh.EnvoyFile {
	return h.sync.FilesOnEnvoy(ctx, envoyID)
}

// EnvoyCryptsyncFiles returns per-envoy cryptsync files for the Swarm Load
// page. Passthrough to the aggregator.
func (h *MeshHandler) EnvoyCryptsyncFiles(ctx context.Context, envoyID string) []cryptsyncmesh.EnvoyFile {
	return h.crypt.FilesOnEnvoy(ctx, envoyID)
}

func (h *MeshHandler) envoyInputsSync() []longdrawermesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]longdrawermesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, longdrawermesh.EnvoyInput{
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

func (h *MeshHandler) handleLongdrawerFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	fleet, err := h.SyncSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "longdrawer fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("longdrawer fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleCryptsyncFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	fleet, err := h.CryptSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cryptsync fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("cryptsync fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleLongdrawerDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.sync.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "longdrawer drilldown failed")
		return
	}
	if files == nil {
		files = []longdrawermesh.FileRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Files []longdrawermesh.FileRow `json:"files"`
	}{Files: files}); err != nil {
		slog.Debug("longdrawer drilldown response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleCryptsyncDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.crypt.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cryptsync drilldown failed")
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
