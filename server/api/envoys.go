// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

// EnvoysHandler provides REST endpoints for envoy management operations
// that are not covered by grpc-gateway.
type EnvoysHandler struct {
	queries     *db.Queries
	fleetIndex  *fleet.FleetIndex
	auditWriter auditRecorder
}

// NewEnvoysHandler creates a EnvoysHandler.
func NewEnvoysHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex) *EnvoysHandler {
	return &EnvoysHandler{queries: queries, fleetIndex: fleetIndex}
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *EnvoysHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// RegisterRoutes registers envoy management endpoints. Reads
// (tags/convergence) require fleet.read; writes (maintenance
// toggle, force update) require fleet.write.
func (h *EnvoysHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	write := auth.RequirePermission(auth.PermFleetWrite)
	mux.Handle("GET /api/v1/envoys/{id}/tags", read(http.HandlerFunc(h.GetTags)))
	mux.Handle("PUT /api/v1/envoys/{id}/maintenance", write(http.HandlerFunc(h.SetMaintenance)))
	mux.Handle("DELETE /api/v1/envoys/{id}/maintenance", write(http.HandlerFunc(h.ClearMaintenance)))
	mux.Handle("GET /api/v1/envoys/{id}/convergence", read(http.HandlerFunc(h.GetConvergence)))
	mux.Handle("POST /api/v1/envoys/{id}/update", write(http.HandlerFunc(h.ForceUpdate)))
	mux.Handle("POST /api/v1/envoys/update", write(http.HandlerFunc(h.ForceUpdateAll)))
}

// GetTags returns the tags for a envoy. Read-only — tags are derived from
// the hostcrate and synced into FleetIndex on every check-in.
func (h *EnvoysHandler) GetTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex == nil {
		writeError(w, http.StatusNotImplemented, "requires FleetIndex")
		return
	}
	ts := h.fleetIndex.GetEnvoy(id)
	if ts == nil {
		writeError(w, http.StatusNotFound, "envoy not found")
		return
	}
	tags := ts.Tags
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"tags": tags})
}

// SetMaintenance sets a maintenance window for a envoy.
func (h *EnvoysHandler) SetMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Until string `json:"until"` // RFC3339
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	until, err := time.Parse(time.RFC3339, req.Until)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid 'until' timestamp (must be RFC3339)")
		return
	}

	if h.fleetIndex != nil {
		h.fleetIndex.SetMaintenanceUntil(id, until)
	}

	recordAudit(r.Context(), h.auditWriter, "envoy.maintenance_set", actorFromRequest(r), "rest",
		"maintenance set for envoy "+id+" until "+until.Format(time.RFC3339), "")

	writeJSON(w, r, http.StatusOK, map[string]any{
		"maintenance_until": until.Format(time.RFC3339),
	})
}

// ClearMaintenance removes the maintenance window for a envoy.
func (h *EnvoysHandler) ClearMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex != nil {
		h.fleetIndex.ClearMaintenance(id)
	}

	recordAudit(r.Context(), h.auditWriter, "envoy.maintenance_cleared", actorFromRequest(r), "rest",
		"maintenance cleared for envoy "+id, "")

	writeJSON(w, r, http.StatusOK, map[string]any{"maintenance_until": nil})
}

// GetConvergence returns the convergence status for an envoy.
func (h *EnvoysHandler) GetConvergence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex != nil {
		ts := h.fleetIndex.GetEnvoy(id)
		if ts == nil {
			writeError(w, http.StatusNotFound, "envoy not found")
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]any{"status": ts.ConvergenceStatus})
		return
	}

	writeError(w, http.StatusNotImplemented, "convergence requires FleetIndex")
}

// ForceUpdate sets the force_update flag on a single envoy so it downloads
// the latest binary on its next check-in.
func (h *EnvoysHandler) ForceUpdate(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusNotImplemented, "requires FleetIndex")
		return
	}

	id := r.PathValue("id")
	if _, ok, _ := h.fleetIndex.GetEnvoyLite(id); !ok {
		writeError(w, http.StatusNotFound, "envoy not found")
		return
	}

	h.fleetIndex.SetForceUpdate(id, true)

	recordAudit(r.Context(), h.auditWriter, "envoy.force_update", actorFromRequest(r), "rest",
		"forced update for envoy "+id, "")

	writeJSON(w, r, http.StatusOK, map[string]any{"status": "queued"})
}

// ForceUpdateAll sets the force_update flag on all active envoys.
func (h *EnvoysHandler) ForceUpdateAll(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusNotImplemented, "requires FleetIndex")
		return
	}

	count := h.fleetIndex.SetForceUpdateAll()

	recordAudit(r.Context(), h.auditWriter, "envoy.force_update_all", actorFromRequest(r), "rest",
		fmt.Sprintf("forced update on %d envoys", count), "")

	writeJSON(w, r, http.StatusOK, map[string]any{"status": "queued", "count": count})
}
