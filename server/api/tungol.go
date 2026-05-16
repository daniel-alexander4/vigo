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

// TungolStatus is the response for GET /api/v1/tungol/status.
type TungolStatus struct {
	Role      string               `json:"role"`
	SteorraID string               `json:"steorra_id,omitempty"`
	Steorras  []tungol.SpokeStatus `json:"steorras,omitempty"`
}

// TungolHandler serves tungol status endpoints. The underlying
// implementation is still the hub-spoke model from server/tungol
// (Hub + SpokeStatus + SpokeStatuses); the user-visible surface
// (route paths, JSON field names, response struct) speaks the
// peer-equal redesign's tungol/steorra vocabulary.
type TungolHandler struct {
	role      config.TungolRole
	steorraID string
	hub       *tungol.Hub
}

// NewTungolHandler creates a tungol handler.
func NewTungolHandler(role config.TungolRole, steorraID string, hub *tungol.Hub) *TungolHandler {
	return &TungolHandler{role: role, steorraID: steorraID, hub: hub}
}

// RegisterRoutes mounts tungol endpoints on the given mux. Mutations
// (push / reassign / drain) are admin-only — they flip envoy-steorra
// assignments and force fan-out. Read endpoints (status + steorra
// drill-downs) gate on fleet.read; they expose tungol-aggregated
// fleet state.
func (h *TungolHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/tungol/status", read(http.HandlerFunc(h.handleStatus)))
	if h.hub != nil {
		mux.Handle("GET /api/v1/tungol/steorras/{id}", read(http.HandlerFunc(h.handleSteorraDetail)))
		mux.Handle("GET /api/v1/tungol/steorras/{id}/envoys", read(http.HandlerFunc(h.handleSteorraEnvoys)))
		mux.Handle("GET /api/v1/tungol/steorras/{id}/runs", read(http.HandlerFunc(h.handleSteorraRuns)))
		mux.Handle("POST /api/v1/tungol/steorras/{id}/push", admin(http.HandlerFunc(h.handleSteorraPush)))
		mux.Handle("POST /api/v1/tungol/reassign", admin(http.HandlerFunc(h.handleReassign)))
		mux.Handle("POST /api/v1/tungol/steorras/{id}/drain", admin(http.HandlerFunc(h.handleDrain)))
	}
}

func (h *TungolHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	role := string(h.role)
	if role == "" {
		role = "standalone"
	}

	resp := TungolStatus{
		Role:      role,
		SteorraID: h.steorraID,
	}

	if h.hub != nil {
		resp.Steorras = h.hub.SpokeStatuses(r.Context())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *TungolHandler) handleSteorraDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	statuses := h.hub.SpokeStatuses(r.Context())
	for _, s := range statuses {
		if s.ID == id {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s)
			return
		}
	}
	writeError(w, http.StatusNotFound, "steorra not found")
}

func (h *TungolHandler) handleSteorraEnvoys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	envoys, err := h.hub.SpokeListEnvoys(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys": envoys})
}

func (h *TungolHandler) handleSteorraRuns(w http.ResponseWriter, r *http.Request) {
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

func (h *TungolHandler) handleSteorraPush(w http.ResponseWriter, r *http.Request) {
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

func (h *TungolHandler) handleReassign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnvoyID      string `json:"envoy_id"`
		FromSteorraID string `json:"from_steorra_id"`
		ToSteorraID   string `json:"to_steorra_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EnvoyID == "" || req.FromSteorraID == "" || req.ToSteorraID == "" {
		writeError(w, http.StatusBadRequest, "envoy_id, from_steorra_id, and to_steorra_id are required")
		return
	}

	result, err := h.hub.ReassignEnvoy(r.Context(), req.EnvoyID, req.FromSteorraID, req.ToSteorraID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *TungolHandler) handleDrain(w http.ResponseWriter, r *http.Request) {
	steorraID := r.PathValue("id")
	var req struct {
		TargetSteorraID string `json:"target_steorra_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	result, err := h.hub.DrainSpoke(r.Context(), steorraID, req.TargetSteorraID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
