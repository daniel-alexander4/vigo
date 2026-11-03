// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm/lockboxmesh"
	"git.alexander4.io/vigo/server/swarm/longdrawermesh"
)

// MeshHandler serves the fleet-wide longdrawer + lockbox mesh
// observability endpoints. Both aggregators live alongside because
// the UI renders them on a single `/mesh` page with subsystem tabs.
type MeshHandler struct {
	fleetIndex *fleet.FleetIndex
	sync       *longdrawermesh.Aggregator
	crypt      *lockboxmesh.Aggregator
	authMethod string
}

func NewMeshHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries, authMethod string) *MeshHandler {
	return &MeshHandler{
		fleetIndex: fleetIndex,
		sync:       longdrawermesh.NewAggregator(queries),
		crypt:      lockboxmesh.NewAggregator(queries),
		authMethod: authMethod,
	}
}

// RegisterRoutes mounts mesh endpoints behind the admin RBAC
// middleware — fleet-wide per-user sync and crypt state is only
// visible to admins.
func (h *MeshHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	mux.Handle("GET /api/v1/swarm/longdrawer/fleet", adminMW(http.HandlerFunc(h.handleLongdrawerFleet)))
	mux.Handle("GET /api/v1/swarm/lockbox/fleet", adminMW(http.HandlerFunc(h.handleLockboxFleet)))
	mux.Handle("GET /api/v1/swarm/longdrawer/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleLongdrawerDrilldown)))
	mux.Handle("GET /api/v1/swarm/lockbox/envoy/{id}/users/{user}/files",
		adminMW(http.HandlerFunc(h.handleLockboxDrilldown)))
}

// SyncSnapshot returns the cached longdrawer fleet view. Used by the
// UI handler directly (no HTTP round-trip).
func (h *MeshHandler) SyncSnapshot(ctx context.Context) (*longdrawermesh.Fleet, error) {
	return h.sync.Snapshot(ctx, h.envoyInputsSync())
}

// CryptSnapshot returns the cached lockbox fleet view.
func (h *MeshHandler) CryptSnapshot(ctx context.Context) (*lockboxmesh.Fleet, error) {
	return h.crypt.Snapshot(ctx, h.envoyInputsCrypt())
}

// EnvoyLongdrawerFiles returns per-envoy longdrawer files for the Swarm Load
// page. Passthrough to the aggregator so the UI handler can consume
// it through the MeshSource interface without importing longdrawermesh.
func (h *MeshHandler) EnvoyLongdrawerFiles(ctx context.Context, envoyID string) []longdrawermesh.EnvoyFile {
	return h.sync.FilesOnEnvoy(ctx, envoyID)
}

// EnvoyLockboxFiles returns per-envoy lockbox files for the Swarm Load
// page. Passthrough to the aggregator.
func (h *MeshHandler) EnvoyLockboxFiles(ctx context.Context, envoyID string) []lockboxmesh.EnvoyFile {
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

func (h *MeshHandler) envoyInputsCrypt() []lockboxmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]lockboxmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, lockboxmesh.EnvoyInput{
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
	writeJSON(w, r, http.StatusOK, fleet)
}

func (h *MeshHandler) handleLockboxFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	fleet, err := h.CryptSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lockbox fleet query failed")
		return
	}
	writeJSON(w, r, http.StatusOK, fleet)
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
	writeJSON(w, r, http.StatusOK, struct {
		Files []longdrawermesh.FileRow `json:"files"`
	}{Files: files})
}

func (h *MeshHandler) handleLockboxDrilldown(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	user := r.PathValue("user")
	if envoyID == "" || user == "" {
		writeError(w, http.StatusBadRequest, "envoy id and user required")
		return
	}
	files, err := h.crypt.FullFileList(r.Context(), envoyID, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lockbox drilldown failed")
		return
	}
	if files == nil {
		files = []lockboxmesh.FileRow{}
	}
	writeJSON(w, r, http.StatusOK, struct {
		Files []lockboxmesh.FileRow `json:"files"`
	}{Files: files})
}
