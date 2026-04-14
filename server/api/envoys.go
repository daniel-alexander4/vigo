// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// EnvoysHandler provides REST endpoints for envoy management operations
// that are not covered by grpc-gateway.
type EnvoysHandler struct {
	queries    *db.Queries
	fleetIndex *fleet.FleetIndex
}

// NewEnvoysHandler creates a EnvoysHandler.
func NewEnvoysHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex) *EnvoysHandler {
	return &EnvoysHandler{queries: queries, fleetIndex: fleetIndex}
}

// RegisterRoutes registers envoy management endpoints.
func (h *EnvoysHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/envoys/{id}/tags", h.GetTags)
	mux.HandleFunc("PUT /api/v1/envoys/{id}/maintenance", h.SetMaintenance)
	mux.HandleFunc("DELETE /api/v1/envoys/{id}/maintenance", h.ClearMaintenance)
	mux.HandleFunc("GET /api/v1/envoys/{id}/convergence", h.GetConvergence)
	mux.HandleFunc("POST /api/v1/envoys/{id}/update", h.ForceUpdate)
	mux.HandleFunc("POST /api/v1/envoys/update", h.ForceUpdateAll)
}

// GetTags returns the tags for a envoy. Read-only — tags are derived from
// the matchfile and synced into FleetIndex on every check-in.
func (h *EnvoysHandler) GetTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex == nil {
		writeJSONError(w, http.StatusNotImplemented, "requires FleetIndex")
		return
	}
	ts := h.fleetIndex.GetEnvoy(id)
	if ts == nil {
		writeJSONError(w, http.StatusNotFound, "envoy not found")
		return
	}
	tags := ts.Tags
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// SetMaintenance sets a maintenance window for a envoy.
func (h *EnvoysHandler) SetMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Until string `json:"until"` // RFC3339
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	until, err := time.Parse(time.RFC3339, req.Until)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid 'until' timestamp (must be RFC3339)")
		return
	}

	if h.fleetIndex != nil {
		h.fleetIndex.SetMaintenanceUntil(id, until)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"maintenance_until": until.Format(time.RFC3339),
	})
}

// ClearMaintenance removes the maintenance window for a envoy.
func (h *EnvoysHandler) ClearMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex != nil {
		h.fleetIndex.ClearMaintenance(id)
	}

	writeJSON(w, http.StatusOK, map[string]any{"maintenance_until": nil})
}

// GetConvergence returns the convergence status for an envoy.
func (h *EnvoysHandler) GetConvergence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex != nil {
		ts := h.fleetIndex.GetEnvoy(id)
		if ts == nil {
			writeJSONError(w, http.StatusNotFound, "envoy not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": ts.ConvergenceStatus})
		return
	}

	writeJSONError(w, http.StatusNotImplemented, "convergence requires FleetIndex")
}

// ForceUpdate sets the force_update flag on a single envoy so it downloads
// the latest binary on its next check-in.
func (h *EnvoysHandler) ForceUpdate(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeJSONError(w, http.StatusNotImplemented, "requires FleetIndex")
		return
	}

	id := r.PathValue("id")
	if _, ok, _ := h.fleetIndex.GetEnvoyLite(id); !ok {
		writeJSONError(w, http.StatusNotFound, "envoy not found")
		return
	}

	h.fleetIndex.SetForceUpdate(id, true)
	writeJSON(w, http.StatusOK, map[string]any{"status": "queued"})
}

// ForceUpdateAll sets the force_update flag on all active envoys.
func (h *EnvoysHandler) ForceUpdateAll(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeJSONError(w, http.StatusNotImplemented, "requires FleetIndex")
		return
	}

	count := h.fleetIndex.SetForceUpdateAll()
	writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "count": count})
}
