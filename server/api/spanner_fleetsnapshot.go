// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/spanner"
)

// Spanner Slice 2-ii-a — the fleet-observability snapshot endpoint and
// the fleet↔spanner projection helpers (ADR-026 + the spanner-redesign
// plan). GET /api/v1/spanner/fleet-snapshot serves this bolt's signed
// fleet state (envoy roster + convergence counters) so peers can
// gossip-aggregate fleet-wide admin reads without a gRPC fan-out.
//
// The payload is signed with the local bolt's Ed25519 identity —
// every gossiped spanner payload is signed, this is no exception. A
// pulling peer verifies the signature against this bolt's roster
// pubkey before caching it.

// handleFleetSnapshot builds, signs, and serves this bolt's
// FleetSnapshot from the in-memory fleet index. Requires the local
// bolt identity (to sign) and a fleet index (the observability
// source); 503s when either is absent.
func (h *SpannerAdmissionHandler) handleFleetSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.authenticatePull(w, r) {
		return
	}
	if h.boltID == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet snapshot requires a bolt identity (spanner mode + local secrets backend)")
		return
	}
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet snapshot unavailable — no fleet index")
		return
	}

	snap := &spanner.FleetSnapshot{
		BoltPubkey:  h.localPubkeyHex(),
		Envoys:      FleetEnvoysToLite(h.fleetIndex.AllEnvoys()),
		Convergence: FleetCountersToSpanner(h.fleetIndex.ConvergenceSummary()),
		ServedAt:    time.Now().UnixMilli(),
	}
	if err := spanner.SignFleetSnapshot(h.boltID.SigningKey, snap); err != nil {
		writeError(w, http.StatusInternalServerError, "signing fleet snapshot: "+err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, snap)
}

// fleetEnvoyToLite projects one in-memory fleet.EnvoyState into the
// spanner.EnvoyLite wire shape.
func fleetEnvoyToLite(es fleet.EnvoyState) spanner.EnvoyLite { //nolint:revive // kept unexported; FleetEnvoysToLite is the public entry
	lite := spanner.EnvoyLite{
		ID:                es.ID,
		Hostname:          es.Hostname,
		IP:                es.IP,
		AgentVersion:      es.AgentVersion,
		Revoked:           es.Revoked,
		ConvergenceStatus: es.ConvergenceStatus,
	}
	if !es.EnrolledAt.IsZero() {
		lite.EnrolledAtMs = es.EnrolledAt.UnixMilli()
	}
	if !es.LastSeen.IsZero() {
		lite.LastSeenMs = es.LastSeen.UnixMilli()
	}
	return lite
}

// FleetEnvoysToLite projects a slice of in-memory fleet.EnvoyState
// into the spanner.EnvoyLite wire shape. Exported so the spanner
// admin-read wiring in server.go can build the local-envoy source.
func FleetEnvoysToLite(envoys []fleet.EnvoyState) []spanner.EnvoyLite {
	out := make([]spanner.EnvoyLite, 0, len(envoys))
	for _, es := range envoys {
		out = append(out, fleetEnvoyToLite(es))
	}
	return out
}

// FleetCountersToSpanner converts fleet.ConvergenceCounters into the
// spanner.ConvergenceCounters wire shape (the two have the same axes;
// the type is duplicated to keep server/spanner free of a server/fleet
// dependency). Exported for the spanner admin-read wiring in server.go.
func FleetCountersToSpanner(c fleet.ConvergenceCounters) spanner.ConvergenceCounters {
	return spanner.ConvergenceCounters{
		Total:     c.Total,
		Converged: c.Converged,
		Degraded:  c.Degraded,
		Failed:    c.Failed,
		NoData:    c.NoData,
		Changed:   c.Changed,
		Diverged:  c.Diverged,
		Offline:   c.Offline,
	}
}
