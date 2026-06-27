// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"
	"sort"

	"git.alexander4.io/vigo/server/fleetpressure"
)

// AgentHealthPageData feeds the `/agent-health` fleet page. The summary
// rollup and the per-envoy rows both come from the fleetpressure SoT in
// one walk — no duplicate iteration, no parallel computation, so the
// dashboard rollup card and this table can never disagree.
type AgentHealthPageData struct {
	PageData
	Summary fleetpressure.FleetSummary
	Envoys  []fleetpressure.EnvoyPressure
}

// AgentHealth renders the per-envoy health table — open FDs, host FD%,
// rootfs%, inode%, repo health, composite — sorted worst-first so the
// degraded envoys lead. Rows link to each envoy's detail page.
func (h *Handler) AgentHealth(w http.ResponseWriter, r *http.Request) {
	summary, envoys := h.dataSource.FleetPressureDetail()
	// Sort worst-Health first, then by Hostname for a deterministic
	// secondary order. Unknown sorts last (nothing to look at).
	sort.SliceStable(envoys, func(i, j int) bool {
		hi, hj := healthSortKey(envoys[i].Health), healthSortKey(envoys[j].Health)
		if hi != hj {
			return hi < hj
		}
		return envoys[i].Hostname < envoys[j].Hostname
	})
	data := AgentHealthPageData{
		PageData: PageData{Title: "Agent Health", ActiveNav: "agent-health"},
		Summary:  summary,
		Envoys:   envoys,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "agent-health", data)
}

// healthSortKey orders severities worst-first for the page table:
// Fail (0) < Warn (1) < OK (2) < Unknown (3). Lower wins under
// sort.SliceStable's less-than predicate.
func healthSortKey(s fleetpressure.Severity) int {
	switch s {
	case fleetpressure.Fail:
		return 0
	case fleetpressure.Warn:
		return 1
	case fleetpressure.OK:
		return 2
	default:
		return 3
	}
}
