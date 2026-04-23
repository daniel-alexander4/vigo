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
	"git.alexander4.io/vigo/server/sigilmesh"
	"git.alexander4.io/vigo/server/echomesh"
)

// MeshHandler serves the fleet-wide echo + sigil mesh
// observability endpoints. Both aggregators live alongside because
// the UI renders them on a single `/mesh` page with subsystem tabs.
type MeshHandler struct {
	fleetIndex *fleet.FleetIndex
	sync       *echomesh.Aggregator
	crypt      *sigilmesh.Aggregator
	authMethod string
}

func NewMeshHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries, authMethod string) *MeshHandler {
	return &MeshHandler{
		fleetIndex: fleetIndex,
		sync:       echomesh.NewAggregator(queries),
		crypt:      sigilmesh.NewAggregator(queries),
		authMethod: authMethod,
	}
}

// RegisterRoutes mounts mesh endpoints behind the admin RBAC
// middleware — fleet-wide per-user sync and crypt state is only
// visible to admins.
func (h *MeshHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	mux.Handle("GET /api/v1/echo/fleet", adminMW(http.HandlerFunc(h.handleEchoFleet)))
	mux.Handle("GET /api/v1/sigil/fleet", adminMW(http.HandlerFunc(h.handleSigilFleet)))
	mux.Handle("GET /api/v1/echo/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleEchoDrilldown)))
	mux.Handle("GET /api/v1/sigil/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleSigilDrilldown)))
}

// SyncSnapshot returns the cached echo fleet view. Used by the
// UI handler directly (no HTTP round-trip).
func (h *MeshHandler) SyncSnapshot(ctx context.Context) (*echomesh.Fleet, error) {
	return h.sync.Snapshot(ctx, h.envoyInputsSync())
}

// CryptSnapshot returns the cached sigil fleet view.
func (h *MeshHandler) CryptSnapshot(ctx context.Context) (*sigilmesh.Fleet, error) {
	return h.crypt.Snapshot(ctx, h.envoyInputsCrypt())
}

func (h *MeshHandler) envoyInputsSync() []echomesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]echomesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, echomesh.EnvoyInput{
			ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked,
		})
	}
	return out
}

func (h *MeshHandler) envoyInputsCrypt() []sigilmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]sigilmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, sigilmesh.EnvoyInput{
			ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked,
		})
	}
	return out
}

func (h *MeshHandler) handleEchoFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.SyncSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "echo fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("echo fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleSigilFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.CryptSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "sigil fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("sigil fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleEchoDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeJSONError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.sync.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "echo drilldown failed")
		return
	}
	if files == nil {
		files = []echomesh.FileRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Files []echomesh.FileRow `json:"files"`
	}{Files: files})
}

func (h *MeshHandler) handleSigilDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeJSONError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.crypt.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "sigil drilldown failed")
		return
	}
	if files == nil {
		files = []sigilmesh.FileRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Files []sigilmesh.FileRow `json:"files"`
	}{Files: files})
}
