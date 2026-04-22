// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"

	"git.alexander4.io/vigo/server/vigorepomesh"
)

// VigorepoPageData drives `ui/templates/pages/vigorepo.html`. Carries
// both the list view (Fleet.Labels) and detail view (Label) — the
// template picks which to render based on whether Label is non-nil.
type VigorepoPageData struct {
	PageData
	Health HealthBanner
	Fleet  *vigorepomesh.Fleet
	Label  *vigorepomesh.LabelSummary
	Error  string
}

// Vigorepo serves `GET /vigorepo` (list) and `GET /vigorepo/{label}`
// (detail). Admin-only — fleet-wide per-user repo inventory exposes
// project names and paths that aren't viewer-appropriate.
func (h *Handler) Vigorepo(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	label := r.PathValue("label")
	if label != "" {
		h.vigorepoDetail(w, r, label)
		return
	}
	h.vigorepoList(w, r)
}

func (h *Handler) vigorepoList(w http.ResponseWriter, r *http.Request) {
	data := VigorepoPageData{
		PageData: PageData{Title: "Vigorepo", ActiveNav: "vigorepo"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.vigorepoFleet == nil {
		data.Error = "vigorepo aggregator not available"
		data.Health = finalizeHealthBanner("Vigorepo", nil, "Aggregator unavailable")
		data.Health.Level = "warn"
		h.renderer.Render(w, "vigorepo", data)
		return
	}

	fleet, err := h.vigorepoFleet.FleetSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner("Vigorepo", nil, "Snapshot failed")
		data.Health.Level = "warn"
		h.renderer.Render(w, "vigorepo", data)
		return
	}
	data.Fleet = fleet
	data.Health = buildVigorepoHealth(fleet)
	h.renderer.Render(w, "vigorepo", data)
}

func (h *Handler) vigorepoDetail(w http.ResponseWriter, r *http.Request, label string) {
	data := VigorepoPageData{
		PageData: PageData{Title: "Vigorepo", ActiveNav: "vigorepo"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.vigorepoFleet == nil {
		http.Error(w, "vigorepo aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.vigorepoFleet.FleetSnapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	summary := fleet.LookupLabel(label)
	if summary == nil {
		http.NotFound(w, r)
		return
	}
	data.Label = summary
	// Detail view uses a condensed health banner reflecting only this
	// label's state — the list-view health is not relevant here.
	data.Health = buildVigorepoLabelHealth(summary)
	h.renderer.Render(w, "vigorepo", data)
}

// buildVigorepoHealth — any pending/orphan/stale mirror is a problem.
// Orphan = mirror held by a peer that's no longer selected (wasted
// disk and a potential privacy leak once the publisher rotates keys).
// Pending = selected but not yet materialized (in-flight, usually
// resolves on the next check-in). Stale = mirror not refreshed in
// >30 days; either the peer is dead or the publisher stopped
// producing.
func buildVigorepoHealth(f *vigorepomesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner("Vigorepo", nil, "No data")
	}
	var problems []HealthProblem
	for i := range f.Labels {
		lbl := &f.Labels[i]
		link := "/vigorepo/" + lbl.Label
		if lbl.CountOrphan > 0 {
			problems = append(problems, HealthProblem{
				Kind:     "orphan",
				Subject:  lbl.Label,
				Detail:   fmt.Sprintf("%d peer(s) holding a mirror no longer selected by the publisher", lbl.CountOrphan),
				Link:     link,
				Severity: "err",
			})
		}
		if lbl.CountStale > 0 {
			problems = append(problems, HealthProblem{
				Kind:     "stale",
				Subject:  lbl.Label,
				Detail:   fmt.Sprintf("%d mirror(s) not refreshed in >30 days", lbl.CountStale),
				Link:     link,
				Severity: "warn",
			})
		}
		if pending := lbl.CountSelected - lbl.CountMaterialized; pending > 0 {
			problems = append(problems, HealthProblem{
				Kind:     "pending",
				Subject:  lbl.Label,
				Detail:   fmt.Sprintf("%d mirror(s) selected but not yet materialized", pending),
				Link:     link,
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf("%d label(s), %d mirror(s), %d publisher(s). No orphans, no stale, no pending.",
		f.Totals.Labels, f.Totals.Mirrors, f.Totals.Sources)
	return finalizeHealthBanner("Vigorepo", problems, headline)
}

func buildVigorepoLabelHealth(lbl *vigorepomesh.LabelSummary) HealthBanner {
	var problems []HealthProblem
	if lbl.CountOrphan > 0 {
		problems = append(problems, HealthProblem{Kind: "orphan", Subject: lbl.Label,
			Detail: fmt.Sprintf("%d peer(s) hold this mirror but aren't selected", lbl.CountOrphan), Severity: "err"})
	}
	if lbl.CountStale > 0 {
		problems = append(problems, HealthProblem{Kind: "stale", Subject: lbl.Label,
			Detail: fmt.Sprintf("%d mirror(s) not refreshed in >30 days", lbl.CountStale), Severity: "warn"})
	}
	if pending := lbl.CountSelected - lbl.CountMaterialized; pending > 0 {
		problems = append(problems, HealthProblem{Kind: "pending", Subject: lbl.Label,
			Detail: fmt.Sprintf("%d mirror(s) selected but not yet materialized", pending), Severity: "warn"})
	}
	headline := fmt.Sprintf("%s: %d materialized / %d selected. Healthy.",
		lbl.Label, lbl.CountMaterialized, lbl.CountSelected)
	return finalizeHealthBanner(lbl.Label, problems, headline)
}
