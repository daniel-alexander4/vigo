// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/hex"
	"net/http"

	"git.alexander4.io/vigo/server/fleet"
)

// FleetPubkeysHandler serves the fleet-wide hostname → Ed25519 pubkey map
// that agents use to bootstrap their gitback peer-pubkey TOFU cache.
// Pre-populating the cache from server-distributed data closes the
// first-contact race that lets a LAN-resident attacker beat the real
// envoy to its first announcement and hijack the cache slot.
//
// The pubkeys are broadcast on multicast in every `GITBACK:` line by
// design — they are public-by-construction, so the endpoint is
// deliberately unauthenticated. mTLS at the transport layer prevents
// MITM tampering of the response, which is the only integrity
// property that matters for this surface.
//
// Revoked envoys are excluded — operators retiring a host should not
// see its pubkey re-bootstrapped onto every peer's cache.
type FleetPubkeysHandler struct {
	fleetIndex *fleet.FleetIndex
}

// NewFleetPubkeysHandler constructs a new handler. The FleetIndex is
// the authoritative source for both the hostname↔ID mapping and the
// stored Ed25519 pubkey from registration.
func NewFleetPubkeysHandler(fi *fleet.FleetIndex) *FleetPubkeysHandler {
	return &FleetPubkeysHandler{fleetIndex: fi}
}

// RegisterRoutes wires the single endpoint onto the mux.
func (h *FleetPubkeysHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/fleet/pubkeys", h.Pubkeys)
}

// fleetPubkeysResponse is the JSON shape returned by GET /api/v1/fleet/pubkeys.
type fleetPubkeysResponse struct {
	// Pubkeys maps hostname → lowercase-hex Ed25519 verifying key. One
	// entry per non-revoked enrolled envoy. Stable shape across versions
	// so older agents can consume newer servers' responses.
	Pubkeys map[string]string `json:"pubkeys"`
}

// Pubkeys returns the fleet's hostname → pubkey map. Used by agents on
// startup and on a 15-minute timer to bootstrap their gitback peer
// pubkey cache. Skips revoked envoys (the cleanup pipeline already
// evicts them from active swarm participation, and re-bootstrapping
// their pubkeys onto every peer's cache would just race with the
// eviction).
func (h *FleetPubkeysHandler) Pubkeys(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeJSON(w, http.StatusOK, fleetPubkeysResponse{Pubkeys: map[string]string{}})
		return
	}
	envoys := h.fleetIndex.AllEnvoys()
	resp := fleetPubkeysResponse{Pubkeys: make(map[string]string, len(envoys))}
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		pk := h.fleetIndex.GetPubKey(e.ID)
		if pk == nil {
			continue
		}
		resp.Pubkeys[e.Hostname] = hex.EncodeToString(pk)
	}
	writeJSON(w, http.StatusOK, resp)
}
