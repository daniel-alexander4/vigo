// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"
	"strconv"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/swarmview"
)

// SwarmHandler serves the substrate-level overview endpoints. The
// rollup logic lives in server/swarmview so both this handler and
// the UI page can consume the same snapshot without an import cycle.

type SwarmHandler struct {
	src swarmview.Sources
}

// NewSwarmHandler creates a substrate-overview handler.
func NewSwarmHandler(tracker *swarm.PeerTracker, manifest *swarm.ManifestStore, fleetIndex *fleet.FleetIndex) *SwarmHandler {
	return &SwarmHandler{
		src: swarmview.Sources{
			Tracker:       tracker,
			ManifestStore: manifest,
			FleetSource: func() []swarmview.EnvoyInput {
				if fleetIndex == nil {
					return nil
				}
				envoys := fleetIndex.AllEnvoys()
				out := make([]swarmview.EnvoyInput, 0, len(envoys))
				for _, e := range envoys {
					out = append(out, swarmview.EnvoyInput{
						ID:       e.ID,
						Hostname: e.Hostname,
						LastSeen: e.LastSeen,
						Revoked:  e.Revoked,
						Offline:  e.Offline,
					})
				}
				return out
			},
		},
	}
}

// RegisterRoutes registers `/api/v1/swarm/*` substrate endpoints.
// Status and peers are aggregated fleet observability — fleet.read.
func (h *SwarmHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/swarm/status", read(http.HandlerFunc(h.Status)))
	mux.Handle("GET /api/v1/swarm/peers", read(http.HandlerFunc(h.Peers)))
}

// Status returns the fleet-wide substrate overview.
func (h *SwarmHandler) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, swarmview.SnapshotStatus(h.src))
}

// Peers returns one row per envoy with substrate-participation counts.
func (h *SwarmHandler) Peers(w http.ResponseWriter, r *http.Request) {
	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "footprint"
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, swarmview.SnapshotPeers(h.src, sortKey, limit))
}

// SnapshotStatus exposes the same rollup the REST handler returns.
// ui/swarm.go calls this directly rather than round-tripping through
// the gateway.
func (h *SwarmHandler) SnapshotStatus() swarmview.Status {
	return swarmview.SnapshotStatus(h.src)
}

// SnapshotPeers is the UI-facing counterpart of Peers.
func (h *SwarmHandler) SnapshotPeers(sortKey string, limit int) swarmview.Peers {
	return swarmview.SnapshotPeers(h.src, sortKey, limit)
}
