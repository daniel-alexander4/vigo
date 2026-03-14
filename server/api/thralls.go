// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
)

// ThrallsHandler provides REST endpoints for thrall management operations
// that are not covered by grpc-gateway.
type ThrallsHandler struct {
	queries    *db.Queries
	fleetIndex *fleet.FleetIndex
}

// NewThrallsHandler creates a ThrallsHandler.
func NewThrallsHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex) *ThrallsHandler {
	return &ThrallsHandler{queries: queries, fleetIndex: fleetIndex}
}

// RegisterRoutes registers thrall management endpoints.
func (h *ThrallsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/thralls/{id}/tags", h.SetTags)
	mux.HandleFunc("GET /api/v1/thralls/{id}/tags", h.GetTags)
	mux.HandleFunc("PUT /api/v1/thralls/{id}/maintenance", h.SetMaintenance)
	mux.HandleFunc("DELETE /api/v1/thralls/{id}/maintenance", h.ClearMaintenance)
	mux.HandleFunc("GET /api/v1/thralls/{id}/compliance", h.GetCompliance)
}

// SetTags sets the tags for a thrall.
func (h *ThrallsHandler) SetTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Update DB.
	if err := h.queries.UpdateThrallTags(r.Context(), db.UpdateThrallTagsParams{
		Tags: strings.Join(req.Tags, ","),
		ID:   id,
	}); err != nil {
		writeJSONError(w, http.StatusNotFound, "thrall not found")
		return
	}

	// Update FleetIndex.
	if h.fleetIndex != nil {
		h.fleetIndex.SetTags(id, req.Tags)
	}

	writeJSON(w, http.StatusOK, map[string]any{"tags": req.Tags})
}

// GetTags returns the tags for a thrall.
func (h *ThrallsHandler) GetTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex != nil {
		ts := h.fleetIndex.GetThrall(id)
		if ts == nil {
			writeJSONError(w, http.StatusNotFound, "thrall not found")
			return
		}
		tags := ts.Tags
		if tags == nil {
			tags = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
		return
	}

	thrall, err := h.queries.GetThrallByID(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "thrall not found")
		return
	}
	var tags []string
	if thrall.Tags != "" {
		for _, t := range strings.Split(thrall.Tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// SetMaintenance sets a maintenance window for a thrall.
func (h *ThrallsHandler) SetMaintenance(w http.ResponseWriter, r *http.Request) {
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

// ClearMaintenance removes the maintenance window for a thrall.
func (h *ThrallsHandler) ClearMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex != nil {
		h.fleetIndex.ClearMaintenance(id)
	}

	writeJSON(w, http.StatusOK, map[string]any{"maintenance_until": nil})
}

// GetCompliance returns the compliance status for a thrall.
func (h *ThrallsHandler) GetCompliance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.fleetIndex != nil {
		ts := h.fleetIndex.GetThrall(id)
		if ts == nil {
			writeJSONError(w, http.StatusNotFound, "thrall not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": ts.ComplianceStatus})
		return
	}

	writeJSONError(w, http.StatusNotImplemented, "compliance requires FleetIndex")
}
