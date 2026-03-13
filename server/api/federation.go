// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"encoding/json"
	"net/http"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/federation"
)

// FederationStatus is the response for GET /api/v1/federation/status.
type FederationStatus struct {
	Role    string                   `json:"role"`
	SpokeID string                  `json:"spoke_id,omitempty"`
	Spokes  []federation.SpokeStatus `json:"spokes,omitempty"`
}

// FederationHandler serves federation status endpoints.
type FederationHandler struct {
	role    config.FederationRole
	spokeID string
	hub     *federation.Hub
}

// NewFederationHandler creates a federation handler.
func NewFederationHandler(role config.FederationRole, spokeID string, hub *federation.Hub) *FederationHandler {
	return &FederationHandler{role: role, spokeID: spokeID, hub: hub}
}

// RegisterRoutes mounts federation endpoints on the given mux.
func (h *FederationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/federation/status", h.handleStatus)
	// Spoke drill-down (Phase 2)
	if h.hub != nil {
		mux.HandleFunc("GET /api/v1/federation/spokes/{id}", h.handleSpokeDetail)
		mux.HandleFunc("GET /api/v1/federation/spokes/{id}/thralls", h.handleSpokeThralls)
		mux.HandleFunc("GET /api/v1/federation/spokes/{id}/runs", h.handleSpokeRuns)
		mux.HandleFunc("POST /api/v1/federation/spokes/{id}/push", h.handleSpokePush)
		// Reassignment + drain (Phase 3)
		mux.HandleFunc("POST /api/v1/federation/reassign", h.handleReassign)
		mux.HandleFunc("POST /api/v1/federation/spokes/{id}/drain", h.handleDrain)
	}
}

func (h *FederationHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	role := string(h.role)
	if role == "" {
		role = "standalone"
	}

	resp := FederationStatus{
		Role:    role,
		SpokeID: h.spokeID,
	}

	if h.hub != nil {
		resp.Spokes = h.hub.SpokeStatuses(r.Context())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *FederationHandler) handleSpokeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	statuses := h.hub.SpokeStatuses(r.Context())
	for _, s := range statuses {
		if s.ID == id {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s)
			return
		}
	}
	http.Error(w, "spoke not found", http.StatusNotFound)
}

func (h *FederationHandler) handleSpokeThralls(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	thralls, err := h.hub.SpokeListThralls(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"thralls": thralls})
}

func (h *FederationHandler) handleSpokeRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := int32(50)
	runs, err := h.hub.SpokeListRuns(r.Context(), id, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"runs": runs})
}

func (h *FederationHandler) handleSpokePush(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ThrallID string `json:"thrall_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	count, err := h.hub.SpokeForcePush(r.Context(), id, req.ThrallID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"thralls_notified": count})
}

func (h *FederationHandler) handleReassign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ThrallID    string `json:"thrall_id"`
		FromSpokeID string `json:"from_spoke_id"`
		ToSpokeID   string `json:"to_spoke_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ThrallID == "" || req.FromSpokeID == "" || req.ToSpokeID == "" {
		http.Error(w, "thrall_id, from_spoke_id, and to_spoke_id are required", http.StatusBadRequest)
		return
	}

	result, err := h.hub.ReassignThrall(r.Context(), req.ThrallID, req.FromSpokeID, req.ToSpokeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *FederationHandler) handleDrain(w http.ResponseWriter, r *http.Request) {
	spokeID := r.PathValue("id")
	var req struct {
		TargetSpokeID string `json:"target_spoke_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	result, err := h.hub.DrainSpoke(r.Context(), spokeID, req.TargetSpokeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
