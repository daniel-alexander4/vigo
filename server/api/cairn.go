// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/cairnmesh"
)

// CairnHandler serves the fleet-wide cairn observability
// endpoint. Aggregation lives in `server/cairnmesh` so the UI
// handler can call it without causing an import cycle through
// `server/fleet`.
type CairnHandler struct {
	fleetIndex *fleet.FleetIndex
	agg        *cairnmesh.Aggregator
}

// NewCairnHandler builds the handler.
func NewCairnHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries) *CairnHandler {
	return &CairnHandler{
		fleetIndex: fleetIndex,
		agg:        cairnmesh.NewAggregator(queries),
	}
}

// RegisterRoutes mounts cairn endpoints.
func (h *CairnHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/cairn/fleet", h.handleFleet)
}

// FleetSnapshot returns the cached aggregated view. The UI handler
// calls this directly rather than round-tripping through the REST
// endpoint.
func (h *CairnHandler) FleetSnapshot(ctx context.Context) (*cairnmesh.Fleet, error) {
	return h.agg.Snapshot(ctx, h.envoyInputs())
}

// envoyInputs converts the fleet index's envoy list into the
// minimal `(ID, Hostname)` tuples the aggregator consumes.
func (h *CairnHandler) envoyInputs() []cairnmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]cairnmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, cairnmesh.EnvoyInput{
			ID:       e.ID,
			Hostname: e.Hostname,
			Revoked:  e.Revoked,
		})
	}
	return out
}

func (h *CairnHandler) handleFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cairn fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("cairn fleet response encode failed", "error", err)
	}
}
