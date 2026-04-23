// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"

	"git.alexander4.io/vigo/server/cairnmesh"
)

// CairnPageData drives `ui/templates/pages/cairn.html`. Carries
// both the list view (Fleet.Labels) and detail view (Label) — the
// template picks which to render based on whether Label is non-nil.
type CairnPageData struct {
	PageData
	Health HealthBanner
	Fleet  *cairnmesh.Fleet
	Label  *cairnmesh.LabelSummary
	Error  string
}

// Cairn serves `GET /cairn` (list) and `GET /cairn/{label}`
// (detail). Admin-only — fleet-wide per-user repo inventory exposes
// project names and paths that aren't viewer-appropriate.
func (h *Handler) Cairn(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	label := r.PathValue("label")
	if label != "" {
		h.cairnDetail(w, r, label)
		return
	}
	h.cairnList(w, r)
}

func (h *Handler) cairnList(w http.ResponseWriter, r *http.Request) {
	data := CairnPageData{
		PageData: PageData{Title: "Cairn", ActiveNav: "cairn"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.cairnFleet == nil {
		data.Error = "cairn aggregator not available"
		data.Health = finalizeHealthBanner("Cairn", nil, "Aggregator unavailable")
		data.Health.Level = "warn"
		h.renderer.Render(w, "cairn", data)
		return
	}

	fleet, err := h.cairnFleet.FleetSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner("Cairn", nil, "Snapshot failed")
		data.Health.Level = "warn"
		h.renderer.Render(w, "cairn", data)
		return
	}
	data.Fleet = fleet
	data.Health = buildCairnHealth(fleet)
	h.renderer.Render(w, "cairn", data)
}

func (h *Handler) cairnDetail(w http.ResponseWriter, r *http.Request, label string) {
	data := CairnPageData{
		PageData: PageData{Title: "Cairn", ActiveNav: "cairn"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.cairnFleet == nil {
		http.Error(w, "cairn aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.cairnFleet.FleetSnapshot(r.Context())
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
	data.Health = buildCairnLabelHealth(summary)
	h.renderer.Render(w, "cairn", data)
}

// buildCairnHealth — any pending/orphan/stale mirror is a problem.
// Orphan = mirror held by a peer that's no longer selected (wasted
// disk and a potential privacy leak once the publisher rotates keys).
// Pending = selected but not yet materialized (in-flight, usually
// resolves on the next check-in). Stale = mirror not refreshed in
// >30 days; either the peer is dead or the publisher stopped
// producing.
func buildCairnHealth(f *cairnmesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner("Cairn", nil, "No data")
	}
	var problems []HealthProblem
	for i := range f.Labels {
		lbl := &f.Labels[i]
		link := "/cairn/" + lbl.Label
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
	return finalizeHealthBanner("Cairn", problems, headline)
}

func buildCairnLabelHealth(lbl *cairnmesh.LabelSummary) HealthBanner {
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
