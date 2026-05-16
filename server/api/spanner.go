// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/tungol"
)

// SpannerStatus is the response for GET /api/v1/spanner/status.
type SpannerStatus struct {
	Role    string                  `json:"role"`
	SpokeID string                 `json:"spoke_id,omitempty"`
	Spokes  []tungol.SpokeStatus  `json:"spokes,omitempty"`
}

// SpannerHandler serves spanner status endpoints.
type SpannerHandler struct {
	role    config.TungolRole
	spokeID string
	hub     *tungol.Hub
}

// NewSpannerHandler creates a spanner handler.
func NewSpannerHandler(role config.TungolRole, spokeID string, hub *tungol.Hub) *SpannerHandler {
	return &SpannerHandler{role: role, spokeID: spokeID, hub: hub}
}

// RegisterRoutes mounts spanner endpoints on the given mux. Hub-
// mode mutations (push / reassign / drain) are admin-only — they
// flip envoy-spoke assignments and force fan-out to spokes. Read
// endpoints (status + spoke drill-downs) gate on fleet.read;
// they expose hub-aggregated fleet state.
func (h *SpannerHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/spanner/status", read(http.HandlerFunc(h.handleStatus)))
	// Spoke drill-down (Phase 2)
	if h.hub != nil {
		mux.Handle("GET /api/v1/spanner/spokes/{id}", read(http.HandlerFunc(h.handleSpokeDetail)))
		mux.Handle("GET /api/v1/spanner/spokes/{id}/envoys", read(http.HandlerFunc(h.handleSpokeEnvoys)))
		mux.Handle("GET /api/v1/spanner/spokes/{id}/runs", read(http.HandlerFunc(h.handleSpokeRuns)))
		mux.Handle("POST /api/v1/spanner/spokes/{id}/push", admin(http.HandlerFunc(h.handleSpokePush)))
		// Reassignment + drain (Phase 3)
		mux.Handle("POST /api/v1/spanner/reassign", admin(http.HandlerFunc(h.handleReassign)))
		mux.Handle("POST /api/v1/spanner/spokes/{id}/drain", admin(http.HandlerFunc(h.handleDrain)))
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
	writeError(w, http.StatusNotFound, "spoke not found")
}

func (h *SpannerHandler) handleSpokeEnvoys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	envoys, err := h.hub.SpokeListEnvoys(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys": envoys})
}

func (h *SpannerHandler) handleSpokeRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := int32(50)
	runs, err := h.hub.SpokeListRuns(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"runs": runs})
}

func (h *SpannerHandler) handleSpokePush(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		EnvoyID string `json:"envoy_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	count, err := h.hub.SpokeForcePush(r.Context(), id, req.EnvoyID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys_notified": count})
}

func (h *SpannerHandler) handleReassign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnvoyID    string `json:"envoy_id"`
		FromSpokeID string `json:"from_spoke_id"`
		ToSpokeID   string `json:"to_spoke_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EnvoyID == "" || req.FromSpokeID == "" || req.ToSpokeID == "" {
		writeError(w, http.StatusBadRequest, "envoy_id, from_spoke_id, and to_spoke_id are required")
		return
	}

	result, err := h.hub.ReassignEnvoy(r.Context(), req.EnvoyID, req.FromSpokeID, req.ToSpokeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
