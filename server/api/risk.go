// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/risk"
)

// RiskHandler serves fleet and per-envoy risk scoring endpoints.
type RiskHandler struct {
	fleetIndex   *fleet.FleetIndex
	baseInterval time.Duration
}

// NewRiskHandler creates a RiskHandler.
func NewRiskHandler(fleetIndex *fleet.FleetIndex, baseInterval time.Duration) *RiskHandler {
	return &RiskHandler{
		fleetIndex:   fleetIndex,
		baseInterval: baseInterval,
	}
}

// RegisterRoutes mounts risk endpoints.
func (h *RiskHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/risk", h.handleFleetRisk)
	mux.HandleFunc("GET /api/v1/risk/{envoyID}", h.handleEnvoyRisk)
}

// handleFleetRisk returns the fleet-wide risk summary.
func (h *RiskHandler) handleFleetRisk(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}

	inputs := envoyInputs(h.fleetIndex.AllEnvoys())
	fr := risk.ScoreFleet(inputs, h.baseInterval, 10)
	fr.Envoys = nil // keep response concise

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fr)
}

// handleEnvoyRisk returns the risk score breakdown for a single envoy.
func (h *RiskHandler) handleEnvoyRisk(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}

	envoyID := r.PathValue("envoyID")
	es := h.fleetIndex.GetEnvoy(envoyID)
	if es == nil {
		http.Error(w, `{"error":"envoy not found"}`, http.StatusNotFound)
		return
	}

	er := risk.ScoreEnvoy(envoyToInput(*es), h.baseInterval)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(er)
}

// envoyInputs converts a slice of fleet.EnvoyState to risk.EnvoyInput.
func envoyInputs(states []fleet.EnvoyState) []risk.EnvoyInput {
	inputs := make([]risk.EnvoyInput, len(states))
	for i, es := range states {
		inputs[i] = envoyToInput(es)
	}
	return inputs
}

func envoyToInput(es fleet.EnvoyState) risk.EnvoyInput {
	return risk.EnvoyInput{
		ID:                es.ID,
		Hostname:          es.Hostname,
		Revoked:           es.Revoked,
		LastSeen:          es.LastSeen,
		ConvergenceStatus: es.ConvergenceStatus,
		Traits:            es.Traits,
	}
}
