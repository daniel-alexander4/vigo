// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/vigocryptmesh"
	"git.alexander4.io/vigo/server/vigosyncmesh"
)

// MeshHandler serves the fleet-wide vigosync + vigocrypt mesh
// observability endpoints. Both aggregators live alongside because
// the UI renders them on a single `/mesh` page with subsystem tabs.
type MeshHandler struct {
	fleetIndex *fleet.FleetIndex
	sync       *vigosyncmesh.Aggregator
	crypt      *vigocryptmesh.Aggregator
}

func NewMeshHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries) *MeshHandler {
	return &MeshHandler{
		fleetIndex: fleetIndex,
		sync:       vigosyncmesh.NewAggregator(queries),
		crypt:      vigocryptmesh.NewAggregator(queries),
	}
}

// RegisterRoutes mounts mesh endpoints. Admin-only RBAC is applied
// by the caller (all /api/v1/ routes go through the admin middleware
// in server/main.go).
func (h *MeshHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/vigosync/fleet", h.handleVigosyncFleet)
	mux.HandleFunc("GET /api/v1/vigocrypt/fleet", h.handleVigocryptFleet)
	mux.HandleFunc("GET /api/v1/vigosync/envoy/{id}/users/{user}/files",
		h.handleVigosyncDrilldown)
	mux.HandleFunc("GET /api/v1/vigocrypt/envoy/{id}/users/{user}/files",
		h.handleVigocryptDrilldown)
}

// SyncSnapshot returns the cached vigosync fleet view. Used by the
// UI handler directly (no HTTP round-trip).
func (h *MeshHandler) SyncSnapshot(ctx context.Context) (*vigosyncmesh.Fleet, error) {
	return h.sync.Snapshot(ctx, h.envoyInputsSync())
}

// CryptSnapshot returns the cached vigocrypt fleet view.
func (h *MeshHandler) CryptSnapshot(ctx context.Context) (*vigocryptmesh.Fleet, error) {
	return h.crypt.Snapshot(ctx, h.envoyInputsCrypt())
}

func (h *MeshHandler) envoyInputsSync() []vigosyncmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]vigosyncmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, vigosyncmesh.EnvoyInput{
			ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked,
		})
	}
	return out
}

func (h *MeshHandler) envoyInputsCrypt() []vigocryptmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]vigocryptmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, vigocryptmesh.EnvoyInput{
			ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked,
		})
	}
	return out
}

func (h *MeshHandler) handleVigosyncFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.SyncSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "vigosync fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("vigosync fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleVigocryptFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.CryptSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "vigocrypt fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("vigocrypt fleet response encode failed", "error", err)
	}
}

func (h *MeshHandler) handleVigosyncDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeJSONError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.sync.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "vigosync drilldown failed")
		return
	}
	if files == nil {
		files = []vigosyncmesh.FileRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Files []vigosyncmesh.FileRow `json:"files"`
	}{Files: files})
}

func (h *MeshHandler) handleVigocryptDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeJSONError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.crypt.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "vigocrypt drilldown failed")
		return
	}
	if files == nil {
		files = []vigocryptmesh.FileRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Files []vigocryptmesh.FileRow `json:"files"`
	}{Files: files})
}
