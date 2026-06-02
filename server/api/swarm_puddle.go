// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm/puddlemesh"
)

// PuddleHandler serves the fleet-wide puddle observability endpoints
// (ADR-014). Aggregation lives in `server/swarm/puddlemesh` so the UI
// handler can call it without an import cycle through `server/fleet`.
// The same aggregator instance is shared with the gitback handler (via
// Aggregator) so the friendly-name map has exactly one producer + one
// cache.
type PuddleHandler struct {
	fleetIndex *fleet.FleetIndex
	agg        *puddlemesh.Aggregator
}

// NewPuddleHandler builds the handler with its own puddlemesh
// aggregator.
func NewPuddleHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries) *PuddleHandler {
	return &PuddleHandler{
		fleetIndex: fleetIndex,
		agg:        puddlemesh.NewAggregator(queries),
	}
}

// Aggregator exposes the underlying aggregator so the gitback handler
// can share it for friendly-name resolution — one instance, one cache,
// one source of truth for names.
func (h *PuddleHandler) Aggregator() *puddlemesh.Aggregator { return h.agg }

// RegisterRoutes mounts the puddle endpoints. Fleet view is aggregated
// swarm state — fleet.read.
func (h *PuddleHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/swarm/puddle/fleet", read(http.HandlerFunc(h.handleFleet)))
	// Friendly puddle-name map (ADR-022). Public-by-construction —
	// name↔pubkey bindings, nothing secret — so the agent can fetch it
	// on startup the same way it fetches /api/v1/fleet/pubkeys; mTLS is
	// the transport guard. vigocli reaches the same data via
	// `vigocli swarm puddle list`.
	mux.HandleFunc("GET /api/v1/swarm/puddle/names", h.handlePuddleNames)
}

// FleetSnapshot returns the cached aggregated puddle view. The UI
// handler calls this directly rather than round-tripping through REST.
func (h *PuddleHandler) FleetSnapshot(ctx context.Context) (*puddlemesh.Fleet, error) {
	return h.agg.Snapshot(ctx, h.envoyInputs())
}

func (h *PuddleHandler) envoyInputs() []puddlemesh.EnvoyInput {
	if h.fleetIndex == nil {
		return nil
	}
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]puddlemesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, puddlemesh.EnvoyInput{ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked})
	}
	return out
}

func (h *PuddleHandler) handleFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	out, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "puddle fleet query failed")
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}

// NamesSnapshot returns the cached aggregated friendly-name map. The
// agent's names-cache bootstrap loop and `vigocli swarm puddle list`
// both reach this data via the REST endpoint below.
func (h *PuddleHandler) NamesSnapshot(ctx context.Context) (*puddlemesh.FleetNames, error) {
	return h.agg.NamesSnapshot(ctx, h.envoyInputs())
}

func (h *PuddleHandler) handlePuddleNames(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	out, err := h.NamesSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "puddle name map query failed")
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}
