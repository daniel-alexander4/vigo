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

// RegisterRoutes mounts gitmirror endpoints. Fleet view is
// aggregated mirror state across the swarm — fleet.read.
func (h *GitmirrorHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/swarm/gitmirror/fleet", read(http.HandlerFunc(h.handleFleet)))
	mux.Handle("GET /api/v1/swarm/gitmirror/projects/{project_id}/roster",
		read(http.HandlerFunc(h.handleRoster)))
}

// FleetSnapshot returns the cached aggregated view. The UI handler
// calls this directly rather than round-tripping through the REST
// endpoint.
func (h *GitmirrorHandler) FleetSnapshot(ctx context.Context) (*gitmirrormesh.Fleet, error) {
	return h.agg.Snapshot(ctx, h.envoyInputs())
}

// EnvoyProjects returns this envoy's local-state view of every
// project it holds (handle, project_id, bare-repo size, head refs,
// users with state) for the per-envoy Swarm Load page. Passthrough
// to the aggregator — the UI handler uses this via the
// GitmirrorFleetSource interface so ui/ doesn't need to import
// gitmirrormesh directly. Replaces the legacy EnvoyMirrors shape
// from the pre-ADR-015 publisher/receiver model.
func (h *GitmirrorHandler) EnvoyProjects(ctx context.Context, envoyID string) []gitmirrormesh.EnvoyProject {
	return h.agg.EnvoyProjects(ctx, envoyID)
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
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	fleet, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gitmirror fleet query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fleet); err != nil {
		slog.Debug("gitmirror fleet response encode failed", "error", err)
	}
}

// RosterResponse is the JSON shape returned by
// `GET /api/v1/swarm/gitmirror/projects/<pid>/roster`. It carries the
// canonical member-fleet list for a project plus the metadata helpers
// pull at push time:
//
//   - `members[]` — every authorized member fleet's Ed25519 pubkey
//     (hex). The first entry is always the founder puddle; the helper
//     daemon walks the token chain it received against this list.
//   - `dr_fanout` — `"fleet-wide"` or `"members-only"`. Hints to
//     the caller's fan-out logic which envoys to push to.
//
// Roster is **derived from the gitmirrormesh aggregator** — the
// freshest report wins when hosts disagree, identical to the
// `ProjectSummary` canonical-field rule. Projects no envoy in the
// fleet has reported on yet return 404.
type RosterResponse struct {
	ProjectID          string   `json:"project_id"`
	Handle             string   `json:"handle"`
	FounderPuddlePubkey string   `json:"founder_puddle_pubkey"`
	Members            []string `json:"members"`
	DrFanout           string   `json:"dr_fanout"`
	MemberFleetCount   int      `json:"member_fleet_count"`
}

func (h *GitmirrorHandler) handleRoster(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusInternalServerError, "gitmirror fleet query failed")
		return
	}
	summary := fleet.LookupProject(pidHex)
	if summary == nil {
		writeError(w, http.StatusNotFound,
			"no envoy in the fleet has reported this project")
		return
	}
	resp := RosterResponse{
		ProjectID:          summary.ProjectID,
		Handle:             summary.Handle,
		FounderPuddlePubkey: summary.FounderPuddlePubkey,
		Members:            summary.Members,
		DrFanout:           summary.DrFanout,
		MemberFleetCount:   summary.MemberFleetCount,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Debug("gitmirror roster response encode failed", "error", err)
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
