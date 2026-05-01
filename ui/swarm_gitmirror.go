// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"

	"git.alexander4.io/vigo/server/swarm/gitmirrormesh"
)

// GitmirrorPageData drives `ui/templates/pages/gitmirror.html`. Carries
// both the list view (Fleet.Projects) and detail view (Project) — the
// template picks which to render based on whether Project is non-nil.
type GitmirrorPageData struct {
	PageData
	Health  HealthBanner
	Fleet   *gitmirrormesh.Fleet
	Project *gitmirrormesh.ProjectSummary
	Error   string
}

// Gitmirror serves `GET /gitmirror` (list) and
// `GET /gitmirror/{project_id}` (detail). Admin-only — fleet-wide
// per-user repo inventory exposes project IDs and member fleet
// pubkeys that aren't viewer-appropriate.
//
// `{project_id}` accepts the canonical 64-char-lowercase-hex form OR
// a unique handle prefix (UX convenience for typing — the
// cryptographic identity remains the project_id).
func (h *Handler) Gitmirror(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	id := r.PathValue("project_id")
	if id != "" {
		h.gitmirrorDetail(w, r, id)
		return
	}
	h.gitmirrorList(w, r)
}

func (h *Handler) gitmirrorList(w http.ResponseWriter, r *http.Request) {
	data := GitmirrorPageData{
		PageData: PageData{Title: "Gitmirror", ActiveNav: "gitmirror"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.gitmirrorFleet == nil {
		data.Error = "gitmirror aggregator not available"
		data.Health = finalizeHealthBanner("Gitmirror", nil, "Aggregator unavailable")
		data.Health.Level = "warn"
		h.renderer.Render(w, "gitmirror", data)
		return
	}

	fleet, err := h.gitmirrorFleet.FleetSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner("Gitmirror", nil, "Snapshot failed")
		data.Health.Level = "warn"
		h.renderer.Render(w, "gitmirror", data)
		return
	}
	data.Fleet = fleet
	data.Health = buildGitmirrorHealth(fleet)
	h.renderer.Render(w, "gitmirror", data)
}

func (h *Handler) gitmirrorDetail(w http.ResponseWriter, r *http.Request, id string) {
	data := GitmirrorPageData{
		PageData: PageData{Title: "Gitmirror", ActiveNav: "gitmirror"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.gitmirrorFleet == nil {
		http.Error(w, "gitmirror aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.gitmirrorFleet.FleetSnapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	// Try project_id (canonical) first, then unique-handle-prefix
	// fallback — either lookup is read-only so falling through is
	// safe even if the operator typed the handle by accident.
	summary := fleet.LookupProject(id)
	if summary == nil {
		summary = fleet.LookupByHandle(id)
	}
	if summary == nil {
		http.NotFound(w, r)
		return
	}
	data.Project = summary
	data.Health = buildGitmirrorProjectHealth(summary)
	h.renderer.Render(w, "gitmirror", data)
}

// buildGitmirrorHealth — divergent refs and unanimity-violating
// envelope state are the two flag conditions in the unified-substrate
// model. Divergent refs mean some host fell behind and hasn't caught
// up via fan-out; non-unanimous envelope digests mean revoke / leave
// gossip hasn't fully propagated.
func buildGitmirrorHealth(f *gitmirrormesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner("Gitmirror", nil, "No data")
	}
	var problems []HealthProblem
	nonUnanimousProjects := 0
	for i := range f.Projects {
		p := &f.Projects[i]
		link := "/swarm/gitmirror/" + p.ProjectID
		if len(p.DivergentRefs) > 0 {
			problems = append(problems, HealthProblem{
				Kind:     "divergent",
				Subject:  p.Handle,
				Detail:   fmt.Sprintf("%d ref(s) where hosts disagree on the head sha", len(p.DivergentRefs)),
				Link:     link,
				Severity: "warn",
			})
		}
		if !p.DigestUnanimous && p.HostCount > 1 {
			nonUnanimousProjects++
			problems = append(problems, HealthProblem{
				Kind:     "envelope-divergent",
				Subject:  p.Handle,
				Detail:   "envelope digests differ — revoke / leave gossip hasn't propagated to all hosts",
				Link:     link,
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf(
		"%d project(s) across %d host(s); %d divergent ref(s); %d project(s) with envelope-digest skew.",
		f.Totals.Projects,
		f.Totals.Hosts,
		f.Totals.DivergentRefs,
		nonUnanimousProjects,
	)
	return finalizeHealthBanner("Gitmirror", problems, headline)
}

func buildGitmirrorProjectHealth(p *gitmirrormesh.ProjectSummary) HealthBanner {
	var problems []HealthProblem
	if len(p.DivergentRefs) > 0 {
		problems = append(problems, HealthProblem{
			Kind:     "divergent",
			Subject:  p.Handle,
			Detail:   fmt.Sprintf("%d ref(s) divergent across hosts", len(p.DivergentRefs)),
			Severity: "warn",
		})
	}
	if !p.DigestUnanimous && p.HostCount > 1 {
		problems = append(problems, HealthProblem{
			Kind:     "envelope-divergent",
			Subject:  p.Handle,
			Detail:   "envelope digests differ across hosts",
			Severity: "warn",
		})
	}
	headline := fmt.Sprintf("%s: %d host(s), %d member fleet(s).",
		p.Handle, p.HostCount, p.MemberFleetCount)
	return finalizeHealthBanner(p.Handle, problems, headline)
}
