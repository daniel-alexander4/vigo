// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"fmt"
	"log/slog"
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

// CleanupDispatcher is the small slice of AgentService the cleanup
// route needs — keeps the API package decoupled from the gRPC layer
// (no import cycle, easy to fake in tests). Returns (task-run ID,
// envoy count, err).
type CleanupDispatcher interface {
	DispatchCleanup(ctx context.Context, target string) (string, int, error)
}

type SwarmHandler struct {
	src         swarmview.Sources
	dispatcher  CleanupDispatcher
	auditWriter auditRecorder
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *SwarmHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

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

// SetCleanupDispatcher wires the agent-service dispatcher so the
// cleanup endpoint can fan `vigo swarm _apply-cleanup` out to envoys.
func (h *SwarmHandler) SetCleanupDispatcher(d CleanupDispatcher) {
	h.dispatcher = d
}

// RegisterRoutes registers `/api/v1/swarm/*` substrate endpoints.
// Status and peers are aggregated fleet observability — fleet.read.
// Cleanup is admin-only — it dispatches a destructive (idempotent)
// agent verb to every targeted envoy.
func (h *SwarmHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin()
	mux.Handle("GET /api/v1/swarm/status", read(http.HandlerFunc(h.Status)))
	mux.Handle("GET /api/v1/swarm/peers", read(http.HandlerFunc(h.Peers)))
	mux.Handle("POST /api/v1/swarm/cleanup", admin(http.HandlerFunc(h.Cleanup)))
}

// Status returns the fleet-wide substrate overview.
func (h *SwarmHandler) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, swarmview.SnapshotStatus(h.src))
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
	writeJSON(w, r, http.StatusOK, swarmview.SnapshotPeers(h.src, sortKey, limit))
}

// Cleanup dispatches the hidden `vigo swarm _apply-cleanup` verb to
// every targeted envoy via TaskDispatch. Optional `?target=<spec>`
// query param scopes the sweep to a hostname pattern; default is `*`
// (full fleet). Returns the number of envoys the dispatch reached.
func (h *SwarmHandler) Cleanup(w http.ResponseWriter, r *http.Request) {
	if h.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "swarm cleanup dispatcher not wired")
		return
	}
	target := r.URL.Query().Get("target")
	if target == "" {
		target = "*"
	}
	taskRunID, dispatched, err := h.dispatcher.DispatchCleanup(r.Context(), target)
	if err != nil {
		slog.Warn("swarm cleanup dispatch failed", "target", target, "error", err)
		writeError(w, http.StatusInternalServerError, "cleanup dispatch failed: "+err.Error())
		return
	}
	recordAudit(r.Context(), h.auditWriter, "swarm.cleanup", actorFromRequest(r), "rest",
		fmt.Sprintf("swarm cleanup dispatched to %s (%d envoys, task=%s)", target, dispatched, taskRunID), "")
	writeJSON(w, r, http.StatusAccepted, map[string]any{
		"target":      target,
		"dispatched":  dispatched,
		"task_run_id": taskRunID,
	})
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
