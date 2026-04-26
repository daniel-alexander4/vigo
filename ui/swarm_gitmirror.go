// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"

	"git.alexander4.io/vigo/server/swarm/gitmirrormesh"
)

// GitmirrorPageData drives `ui/templates/pages/gitmirror.html`. Carries
// both the list view (Fleet.Labels) and detail view (Label) — the
// template picks which to render based on whether Label is non-nil.
type GitmirrorPageData struct {
	PageData
	Health HealthBanner
	Fleet  *gitmirrormesh.Fleet
	Label  *gitmirrormesh.LabelSummary
	Error  string
}

// Gitmirror serves `GET /gitmirror` (list) and `GET /gitmirror/{label}`
// (detail). Admin-only — fleet-wide per-user repo inventory exposes
// project names and paths that aren't viewer-appropriate.
func (h *Handler) Gitmirror(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	label := r.PathValue("label")
	if label != "" {
		h.gitmirrorDetail(w, r, label)
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

func (h *Handler) gitmirrorDetail(w http.ResponseWriter, r *http.Request, label string) {
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
	summary := fleet.LookupLabel(label)
	if summary == nil {
		http.NotFound(w, r)
		return
	}
	data.Label = summary
	// Detail view uses a condensed health banner reflecting only this
	// label's state — the list-view health is not relevant here.
	data.Health = buildGitmirrorLabelHealth(summary)
	h.renderer.Render(w, "gitmirror", data)
}

// buildGitmirrorHealth — any pending/orphan/stale mirror is a problem.
// Orphan = mirror held by a peer that's no longer selected (wasted
// disk and a potential privacy leak once the publisher rotates keys).
// Pending = selected but not yet materialized (in-flight, usually
// resolves on the next check-in). Stale = mirror not refreshed in
// >30 days; either the peer is dead or the publisher stopped
// producing.
func buildGitmirrorHealth(f *gitmirrormesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner("Gitmirror", nil, "No data")
	}
	var problems []HealthProblem
	for i := range f.Labels {
		lbl := &f.Labels[i]
		link := "/swarm/gitmirror/" + lbl.Label
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
	return finalizeHealthBanner("Gitmirror", problems, headline)
}

func buildGitmirrorLabelHealth(lbl *gitmirrormesh.LabelSummary) HealthBanner {
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
