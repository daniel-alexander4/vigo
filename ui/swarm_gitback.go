// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"net/http"

	"git.alexander4.io/vigo/server/i18n"
	"git.alexander4.io/vigo/server/swarm/gitbackmesh"
)

// GitbackPageData drives `ui/templates/pages/gitback.html`. Carries
// both the list view (Fleet.Projects) and detail view (Project) — the
// template picks which to render based on whether Project is non-nil.
type GitbackPageData struct {
	PageData
	Health  HealthBanner
	Fleet   *gitbackmesh.Fleet
	Project *gitbackmesh.ProjectSummary
	Error   string
}

// Gitback serves `GET /gitback` (list) and
// `GET /gitback/{project_id}` (detail). Admin-only — fleet-wide
// per-user repo inventory exposes project IDs and member fleet
// pubkeys that aren't viewer-appropriate.
//
// `{project_id}` accepts the canonical 64-char-lowercase-hex form OR
// a unique handle prefix (UX convenience for typing — the
// cryptographic identity remains the project_id).
func (h *Handler) Gitback(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	id := r.PathValue("project_id")
	if id != "" {
		h.gitbackDetail(w, r, id)
		return
	}
	h.gitbackList(w, r)
}

func (h *Handler) gitbackList(w http.ResponseWriter, r *http.Request) {
	data := GitbackPageData{
		PageData: PageData{Title: "Gitback", ActiveNav: "gitback"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.gitbackFleet == nil {
		data.Error = i18n.CatalogT(data.Locale, "health_banner.mesh_unavailable")
		data.Health = finalizeHealthBanner(data.Locale, "Gitback", nil, i18n.CatalogT(data.Locale, "health_banner.aggregator_unavailable"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "gitback", data)
		return
	}

	fleet, err := h.gitbackFleet.FleetSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner(data.Locale, "Gitback", nil, i18n.CatalogT(data.Locale, "health_banner.snapshot_failed"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "gitback", data)
		return
	}
	data.Fleet = fleet
	data.Health = buildGitbackHealth(data.Locale, fleet)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "gitback", data)
}

func (h *Handler) gitbackDetail(w http.ResponseWriter, r *http.Request, id string) {
	data := GitbackPageData{
		PageData: PageData{Title: "Gitback", ActiveNav: "gitback"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.gitbackFleet == nil {
		http.Error(w, "gitback aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.gitbackFleet.FleetSnapshot(r.Context())
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
	data.Health = buildGitbackProjectHealth(data.Locale, summary)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "gitback", data)
}

// buildGitbackHealth — divergent refs and unanimity-violating
// envelope state are the two flag conditions in the unified-substrate
// model. Divergent refs mean some host fell behind and hasn't caught
// up via fan-out; non-unanimous envelope digests mean revoke / leave
// gossip hasn't fully propagated.
func buildGitbackHealth(locale string, f *gitbackmesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner(locale, "Gitback", nil, i18n.CatalogT(locale, "health_banner.no_data"))
	}
	var problems []HealthProblem
	nonUnanimousProjects := 0
	for i := range f.Projects {
		p := &f.Projects[i]
		link := "/swarm/gitback/" + p.ProjectID
		if len(p.DivergentRefs) > 0 {
			problems = append(problems, HealthProblem{
				Kind:     "divergent",
				Subject:  p.Handle,
				Detail:   fmt.Sprintf(i18n.CatalogT(locale, "swarm.gitback.divergent_detail"), len(p.DivergentRefs)),
				Link:     link,
				Severity: "warn",
			})
		}
		if !p.DigestUnanimous && p.HostCount > 1 {
			nonUnanimousProjects++
			problems = append(problems, HealthProblem{
				Kind:     "envelope-divergent",
				Subject:  p.Handle,
				Detail:   i18n.CatalogT(locale, "swarm.gitback.envelope_divergent_detail"),
				Link:     link,
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf(
		i18n.CatalogT(locale, "swarm.gitback.headline"),
		f.Totals.Projects,
		f.Totals.Hosts,
		f.Totals.DivergentRefs,
		nonUnanimousProjects,
	)
	return finalizeHealthBanner(locale, "Gitback", problems, headline)
}

func buildGitbackProjectHealth(locale string, p *gitbackmesh.ProjectSummary) HealthBanner {
	var problems []HealthProblem
	if len(p.DivergentRefs) > 0 {
		problems = append(problems, HealthProblem{
			Kind:     "divergent",
			Subject:  p.Handle,
			Detail:   fmt.Sprintf(i18n.CatalogT(locale, "swarm.gitback.project_divergent_detail"), len(p.DivergentRefs)),
			Severity: "warn",
		})
	}
	if !p.DigestUnanimous && p.HostCount > 1 {
		problems = append(problems, HealthProblem{
			Kind:     "envelope-divergent",
			Subject:  p.Handle,
			Detail:   i18n.CatalogT(locale, "swarm.gitback.project_envelope_divergent_detail"),
			Severity: "warn",
		})
	}
	headline := fmt.Sprintf(i18n.CatalogT(locale, "swarm.gitback.project_headline"),
		p.Handle, p.HostCount, p.MemberFleetCount)
	return finalizeHealthBanner(locale, p.Handle, problems, headline)
}
