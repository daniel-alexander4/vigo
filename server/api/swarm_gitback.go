// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm/gitbackmesh"
	"git.alexander4.io/vigo/server/swarm/puddlemesh"
)

// GitbackHandler serves the fleet-wide gitback observability
// endpoint. Aggregation lives in `server/gitbackmesh` so the UI
// handler can call it without causing an import cycle through
// `server/fleet`.
type GitbackHandler struct {
	fleetIndex *fleet.FleetIndex
	agg        *gitbackmesh.Aggregator
}

// NewGitbackHandler builds the handler. `puddleAgg` (shared with the
// puddle handler) supplies the friendly-name map for the
// `gitback://<name>/<handle>` display hint — puddlemesh owns names now.
func NewGitbackHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries, puddleAgg *puddlemesh.Aggregator) *GitbackHandler {
	return &GitbackHandler{
		fleetIndex: fleetIndex,
		agg:        gitbackmesh.NewAggregator(queries, puddleAgg),
	}
}

// RegisterRoutes mounts gitback endpoints. Fleet view is
// aggregated mirror state across the swarm — fleet.read.
func (h *GitbackHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/swarm/gitback/fleet", read(http.HandlerFunc(h.handleFleet)))
	mux.Handle("GET /api/v1/swarm/gitback/projects/{project_id}/roster",
		read(http.HandlerFunc(h.handleRoster)))
}

// FleetSnapshot returns the cached aggregated view. The UI handler
// calls this directly rather than round-tripping through the REST
// endpoint.
func (h *GitbackHandler) FleetSnapshot(ctx context.Context) (*gitbackmesh.Fleet, error) {
	return h.agg.Snapshot(ctx, h.envoyInputs())
}

// EnvoyProjects returns this envoy's local-state view of every
// project it holds (handle, project_id, bare-repo size, head refs,
// users with state) for the per-envoy Swarm Load page. Passthrough
// to the aggregator — the UI handler uses this via the
// GitbackFleetSource interface so ui/ doesn't need to import
// gitbackmesh directly. Replaces the legacy EnvoyMirrors shape
// from the pre-ADR-015 publisher/receiver model.
func (h *GitbackHandler) EnvoyProjects(ctx context.Context, envoyID string) []gitbackmesh.EnvoyProject {
	return h.agg.EnvoyProjects(ctx, envoyID)
}

// envoyInputs converts the fleet index's envoy list into the
// minimal `(ID, Hostname)` tuples the aggregator consumes.
func (h *GitbackHandler) envoyInputs() []gitbackmesh.EnvoyInput {
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]gitbackmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, gitbackmesh.EnvoyInput{
			ID:       e.ID,
			Hostname: e.Hostname,
			Revoked:  e.Revoked,
		})
	}
	return out
}

func (h *GitbackHandler) handleFleet(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet index not available")
		return
	}
	fleet, err := h.FleetSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gitback fleet query failed")
		return
	}
	writeJSON(w, r, http.StatusOK, fleet)
}

// RosterResponse is the JSON shape returned by
// `GET /api/v1/swarm/gitback/projects/<pid>/roster`. It carries the
// canonical member-fleet list for a project plus the metadata helpers
// pull at push time:
//
//   - `members[]` — every authorized member fleet's Ed25519 pubkey
//     (hex). The first entry is always the founder puddle; the helper
//     daemon walks the token chain it received against this list.
//   - `dr_scope` — `"swarm-wide"` or `"members-only"`. Hints to
//     the caller's fan-out logic which envoys to push to.
//
// Roster is **derived from the gitbackmesh aggregator** — the
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

func (h *GitbackHandler) handleRoster(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusInternalServerError, "gitback fleet query failed")
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
	writeJSON(w, r, http.StatusOK, resp)
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
