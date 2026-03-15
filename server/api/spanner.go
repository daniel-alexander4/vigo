// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/spanner"
)

// SpannerStatus is the response for GET /api/v1/spanner/status.
type SpannerStatus struct {
	Role    string                  `json:"role"`
	SpokeID string                 `json:"spoke_id,omitempty"`
	Spokes  []spanner.SpokeStatus  `json:"spokes,omitempty"`
}

// SpannerHandler serves spanner status endpoints.
type SpannerHandler struct {
	role    config.SpannerRole
	spokeID string
	hub     *spanner.Hub
}

// NewSpannerHandler creates a spanner handler.
func NewSpannerHandler(role config.SpannerRole, spokeID string, hub *spanner.Hub) *SpannerHandler {
	return &SpannerHandler{role: role, spokeID: spokeID, hub: hub}
}

// RegisterRoutes mounts spanner endpoints on the given mux.
func (h *SpannerHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/spanner/status", h.handleStatus)
	// Spoke drill-down (Phase 2)
	if h.hub != nil {
		mux.HandleFunc("GET /api/v1/spanner/spokes/{id}", h.handleSpokeDetail)
		mux.HandleFunc("GET /api/v1/spanner/spokes/{id}/thralls", h.handleSpokeThralls)
		mux.HandleFunc("GET /api/v1/spanner/spokes/{id}/runs", h.handleSpokeRuns)
		mux.HandleFunc("POST /api/v1/spanner/spokes/{id}/push", h.handleSpokePush)
		// Reassignment + drain (Phase 3)
		mux.HandleFunc("POST /api/v1/spanner/reassign", h.handleReassign)
		mux.HandleFunc("POST /api/v1/spanner/spokes/{id}/drain", h.handleDrain)
	}
}

func (h *SpannerHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	role := string(h.role)
	if role == "" {
		role = "standalone"
	}

	resp := SpannerStatus{
		Role:    role,
		SpokeID: h.spokeID,
	}

	if h.hub != nil {
		resp.Spokes = h.hub.SpokeStatuses(r.Context())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *SpannerHandler) handleSpokeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	statuses := h.hub.SpokeStatuses(r.Context())
	for _, s := range statuses {
		if s.ID == id {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s)
			return
		}
	}
	writeJSONError(w, http.StatusNotFound, "spoke not found")
}

func (h *SpannerHandler) handleSpokeThralls(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	thralls, err := h.hub.SpokeListThralls(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"thralls": thralls})
}

func (h *SpannerHandler) handleSpokeRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := int32(50)
	runs, err := h.hub.SpokeListRuns(r.Context(), id, limit)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"runs": runs})
}

func (h *SpannerHandler) handleSpokePush(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ThrallID string `json:"thrall_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	count, err := h.hub.SpokeForcePush(r.Context(), id, req.ThrallID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"thralls_notified": count})
}

func (h *SpannerHandler) handleReassign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ThrallID    string `json:"thrall_id"`
		FromSpokeID string `json:"from_spoke_id"`
		ToSpokeID   string `json:"to_spoke_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ThrallID == "" || req.FromSpokeID == "" || req.ToSpokeID == "" {
		writeJSONError(w, http.StatusBadRequest, "thrall_id, from_spoke_id, and to_spoke_id are required")
		return
	}

	result, err := h.hub.ReassignThrall(r.Context(), req.ThrallID, req.FromSpokeID, req.ToSpokeID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *SpannerHandler) handleDrain(w http.ResponseWriter, r *http.Request) {
	spokeID := r.PathValue("id")
	var req struct {
		TargetSpokeID string `json:"target_spoke_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	result, err := h.hub.DrainSpoke(r.Context(), spokeID, req.TargetSpokeID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
