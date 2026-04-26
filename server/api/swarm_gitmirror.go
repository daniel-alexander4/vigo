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
	"git.alexander4.io/vigo/server/swarm/gitmirrormesh"
)

// GitmirrorHandler serves the fleet-wide gitmirror observability
// endpoint. Aggregation lives in `server/gitmirrormesh` so the UI
// handler can call it without causing an import cycle through
// `server/fleet`.
type GitmirrorHandler struct {
	fleetIndex *fleet.FleetIndex
	agg        *gitmirrormesh.Aggregator
}

// NewGitmirrorHandler builds the handler.
func NewGitmirrorHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries) *GitmirrorHandler {
	return &GitmirrorHandler{
		fleetIndex: fleetIndex,
		agg:        gitmirrormesh.NewAggregator(queries),
	}
}

// RegisterRoutes mounts gitmirror endpoints.
func (h *GitmirrorHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/swarm/gitmirror/fleet", h.handleFleet)
}

// FleetSnapshot returns the cached aggregated view. The UI handler
// calls this directly rather than round-tripping through the REST
// endpoint.
func (h *GitmirrorHandler) FleetSnapshot(ctx context.Context) (*gitmirrormesh.Fleet, error) {
	return h.agg.Snapshot(ctx, h.envoyInputs())
}

// EnvoyMirrors returns this envoy's receiver-side mirrors with
// sizes for the per-envoy Swarm Load page. Passthrough to the
// aggregator — the UI handler uses this via the GitmirrorFleetSource
// interface so ui/ doesn't need to import gitmirrormesh.
func (h *GitmirrorHandler) EnvoyMirrors(ctx context.Context, envoyID string) []gitmirrormesh.EnvoyMirror {
	return h.agg.EnvoyMirrors(ctx, envoyID)
}

// envoyInputs converts the fleet index's envoy list into the
// minimal `(ID, Hostname)` tuples the aggregator consumes.
func (h *GitmirrorHandler) envoyInputs() []gitmirrormesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]gitmirrormesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, gitmirrormesh.EnvoyInput{
			ID:       e.ID,
			Hostname: e.Hostname,
			Revoked:  e.Revoked,
		})
	}
	return out
}

func (h *GitmirrorHandler) handleFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "gitmirror fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("gitmirror fleet response encode failed", "error", err)
	}
}
