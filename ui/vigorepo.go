// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/daniel-alexander4/vigo/server/vigorepomesh"
)

// VigorepoPageData backs the `/vigorepo` page.
type VigorepoPageData struct {
	PageData
	Totals       vigorepomesh.Totals
	Labels       []vigorepomesh.LabelSummary
	Participants []string
	StatusFilter string
	Search       string
	HasFleet     bool
}

// Vigorepo renders the fleet-wide vigorepo observability page.
func (h *Handler) Vigorepo(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	search := strings.TrimSpace(r.URL.Query().Get("q"))

	data := VigorepoPageData{
		PageData:     PageData{Title: "Vigorepo", ActiveNav: "vigorepo"},
		StatusFilter: statusFilter,
		Search:       search,
	}

	if h.vigorepoFleet != nil {
		fleet, err := h.vigorepoFleet.FleetSnapshot(r.Context())
		if err != nil {
			slog.Warn("vigorepo: fleet snapshot failed", "error", err)
		} else {
			data.HasFleet = true
			data.Totals = fleet.Totals
			data.Participants = fleet.Participants
			data.Labels = filterLabels(fleet.Labels, statusFilter, search)
			sortLabels(data.Labels)
		}
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "vigorepo", data)
}

// VigorepoLabelsFragment serves the HTMX table fragment for
// status/search filtering.
func (h *Handler) VigorepoLabelsFragment(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	search := strings.TrimSpace(r.URL.Query().Get("q"))

	data := VigorepoPageData{
		StatusFilter: statusFilter,
		Search:       search,
	}
	if h.vigorepoFleet != nil {
		fleet, err := h.vigorepoFleet.FleetSnapshot(r.Context())
		if err == nil {
			data.HasFleet = true
			data.Labels = filterLabels(fleet.Labels, statusFilter, search)
			sortLabels(data.Labels)
		}
	}

	h.renderer.RenderFragment(w, "vigorepo", "vigorepo-labels-fragment", data)
}

// filterLabels applies the status + search filters and returns a
// fresh slice. A label passes the status filter iff at least one of
// its mirror presences is in that status. Status "paused" is a
// label-level flag. Empty filter means "all".
func filterLabels(labels []vigorepomesh.LabelSummary, status, search string) []vigorepomesh.LabelSummary {
	out := make([]vigorepomesh.LabelSummary, 0, len(labels))
	lower := strings.ToLower(search)
	for _, l := range labels {
		if search != "" {
			if !strings.Contains(strings.ToLower(l.Label), lower) &&
				!strings.Contains(strings.ToLower(l.Publisher), lower) &&
				!strings.Contains(strings.ToLower(l.Project), lower) {
				continue
			}
		}
		switch status {
		case "", "all":
			// no-op
		case "paused":
			if !l.Paused {
				continue
			}
		case "active":
			if l.CountMaterialized == 0 {
				continue
			}
		case "pending":
			if l.CountSelected <= l.CountMaterialized {
				continue
			}
		case "orphan":
			if l.CountOrphan == 0 {
				continue
			}
		case "stale":
			if l.CountStale == 0 {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

func sortLabels(labels []vigorepomesh.LabelSummary) {
	sort.Slice(labels, func(i, j int) bool {
		return labels[i].Label < labels[j].Label
	})
}
