// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm/swarmgitmesh"
)

// SwarmgitHandler serves the fleet-wide swarmgit observability
// endpoint. Aggregation lives in `server/swarmgitmesh` so the UI
// handler can call it without causing an import cycle through
// `server/fleet`.
type SwarmgitHandler struct {
	fleetIndex *fleet.FleetIndex
	agg        *swarmgitmesh.Aggregator
}

// NewSwarmgitHandler builds the handler.
func NewSwarmgitHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries) *SwarmgitHandler {
	return &SwarmgitHandler{
		fleetIndex: fleetIndex,
		agg:        swarmgitmesh.NewAggregator(queries),
	}
}

// RegisterRoutes mounts swarmgit endpoints. Fleet view is
// aggregated mirror state across the swarm — fleet.read.
func (h *SwarmgitHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/swarm/swarmgit/fleet", read(http.HandlerFunc(h.handleFleet)))
	mux.Handle("GET /api/v1/swarm/swarmgit/projects/{project_id}/roster",
		read(http.HandlerFunc(h.handleRoster)))
	// Friendly puddle-name map (ADR-022). Public-by-construction —
	// name↔pubkey bindings, nothing secret — so the agent can fetch
	// it on startup the same way it fetches /api/v1/fleet/pubkeys;
	// mTLS is the transport guard. vigocli reaches the same data via
	// `vigocli swarm puddle names list`.
	mux.HandleFunc("GET /api/v1/swarm/puddle/names", h.handlePuddleNames)
}

// FleetSnapshot returns the cached aggregated view. The UI handler
// calls this directly rather than round-tripping through the REST
// endpoint.
func (h *SwarmgitHandler) FleetSnapshot(ctx context.Context) (*swarmgitmesh.Fleet, error) {
	return h.agg.Snapshot(ctx, h.envoyInputs())
}

// EnvoyProjects returns this envoy's local-state view of every
// project it holds (handle, project_id, bare-repo size, head refs,
// users with state) for the per-envoy Swarm Load page. Passthrough
// to the aggregator — the UI handler uses this via the
// SwarmgitFleetSource interface so ui/ doesn't need to import
// swarmgitmesh directly. Replaces the legacy EnvoyMirrors shape
// from the pre-ADR-015 publisher/receiver model.
func (h *SwarmgitHandler) EnvoyProjects(ctx context.Context, envoyID string) []swarmgitmesh.EnvoyProject {
	return h.agg.EnvoyProjects(ctx, envoyID)
}

// envoyInputs converts the fleet index's envoy list into the
// minimal `(ID, Hostname)` tuples the aggregator consumes.
func (h *SwarmgitHandler) envoyInputs() []swarmgitmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]swarmgitmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, swarmgitmesh.EnvoyInput{
			ID:       e.ID,
			Hostname: e.Hostname,
			Revoked:  e.Revoked,
		})
	}
	return out
}

func (h *SwarmgitHandler) handleFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	fleet, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "swarmgit fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("swarmgit fleet response encode failed", "error", err)
	}
}

// PuddleNamesSnapshot returns the cached aggregated friendly-name
// map. The agent's names-cache bootstrap loop and `vigocli swarm
// puddle names list` both call through this.
func (h *SwarmgitHandler) PuddleNamesSnapshot(ctx context.Context) (*swarmgitmesh.FleetNames, error) {
	return h.agg.NamesSnapshot(ctx, h.envoyInputs())
}

func (h *SwarmgitHandler) handlePuddleNames(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	out, err := h.PuddleNamesSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "puddle name map query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		slog.Debug("puddle names response encode failed", "error", err)
	}
}

// RosterResponse is the JSON shape returned by
// `GET /api/v1/swarm/swarmgit/projects/<pid>/roster`. It carries the
// canonical member-fleet list for a project plus the metadata helpers
// pull at push time:
//
//   - `members[]` — every authorized member fleet's Ed25519 pubkey
//     (hex). The first entry is always the founder puddle; the helper
//     daemon walks the token chain it received against this list.
//   - `dr_scope` — `"swarm-wide"` or `"members-only"`. Hints to
//     the caller's fan-out logic which envoys to push to.
//
// Roster is **derived from the swarmgitmesh aggregator** — the
// freshest report wins when hosts disagree, identical to the
// `ProjectSummary` canonical-field rule. Projects no envoy in the
// fleet has reported on yet return 404.
type RosterResponse struct {
	ProjectID           string   `json:"project_id"`
	Handle              string   `json:"handle"`
	FounderPuddlePubkey string   `json:"founder_puddle_pubkey"`
	Members             []string `json:"members"`
	DrScope             string   `json:"dr_scope"`
	MemberFleetCount    int      `json:"member_fleet_count"`
}

func (h *SwarmgitHandler) handleRoster(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	pidHex := r.PathValue("project_id")
	if !validProjectIDHex(pidHex) {
		writeError(w, http.StatusBadRequest,
			"project_id must be 64-char lowercase hex")
		return
	}
	fleet, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "swarmgit fleet query failed")
		return
	}
	summary := fleet.LookupProject(pidHex)
	if summary == nil {
		writeError(w, http.StatusNotFound,
			"no envoy in the fleet has reported this project")
		return
	}
	resp := RosterResponse{
		ProjectID:           summary.ProjectID,
		Handle:              summary.Handle,
		FounderPuddlePubkey: summary.FounderPuddlePubkey,
		Members:             summary.Members,
		DrScope:             summary.DrScope,
		MemberFleetCount:    summary.MemberFleetCount,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Debug("swarmgit roster response encode failed", "error", err)
	}
}

// validProjectIDHex enforces the URL-segment shape: exactly 64
// lowercase hex chars. Identical predicate to
// `swarm.is_project_id_path_segment` on the swarm peer-port; both
// share the same hex form because the same project_id rides over
// both surfaces.
func validProjectIDHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
