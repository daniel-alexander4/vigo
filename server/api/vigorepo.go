// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/vigorepomesh"
)

// VigorepoHandler serves the fleet-wide vigorepo observability
// endpoint. Aggregation lives in `server/vigorepomesh` so the UI
// handler can call it without causing an import cycle through
// `server/fleet`.
type VigorepoHandler struct {
	fleetIndex *fleet.FleetIndex
	agg        *vigorepomesh.Aggregator
}

// NewVigorepoHandler builds the handler.
func NewVigorepoHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries) *VigorepoHandler {
	return &VigorepoHandler{
		fleetIndex: fleetIndex,
		agg:        vigorepomesh.NewAggregator(queries),
	}
}

// RegisterRoutes mounts vigorepo endpoints.
func (h *VigorepoHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/vigorepo/fleet", h.handleFleet)
}

// FleetSnapshot returns the cached aggregated view. The UI handler
// calls this directly rather than round-tripping through the REST
// endpoint.
func (h *VigorepoHandler) FleetSnapshot(ctx context.Context) (*vigorepomesh.Fleet, error) {
	return h.agg.Snapshot(ctx, h.envoyInputs())
}

// envoyInputs converts the fleet index's envoy list into the
// minimal `(ID, Hostname)` tuples the aggregator consumes.
func (h *VigorepoHandler) envoyInputs() []vigorepomesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]vigorepomesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, vigorepomesh.EnvoyInput{
			ID:       e.ID,
			Hostname: e.Hostname,
			Revoked:  e.Revoked,
		})
	}
	return out
}

func (h *VigorepoHandler) handleFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "vigorepo fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fleet)
}
