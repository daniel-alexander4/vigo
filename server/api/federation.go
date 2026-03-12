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
