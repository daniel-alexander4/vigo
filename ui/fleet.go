// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"

	"git.alexander4.io/vigo/server/fleetpressure"
	"git.alexander4.io/vigo/server/spanner"
)

// FleetOverviewData feeds the `/fleet` overview page (ADR-037) — the
// dedicated fleet-wide aggregate surface. Every per-bolt UI page reads only
// this bolt's partition; this page is the one place that fans out across the
// spanner and merges. It reuses the same federated summaries the dashboard
// cards already compute (scopedSecurity / scopedRisk / scopedCompliance /
// scopedHealth at fleet scope), so the overview can never drift from them.
// The per-*OK flags gate each card: a fan-out that failed renders nothing
// rather than a misleading zero.
type FleetOverviewData struct {
	PageData
	BoltCount    int
	EnvoyCount   int
	Convergence  spanner.ConvergenceCounters
	Security     spanner.SecuritySummary
	SecurityOK   bool
	Risk         spanner.RiskSummary
	RiskOK       bool
	Compliance   spanner.ComplianceSummary
	ComplianceOK bool
	Health       fleetpressure.FleetSummary
	HealthOK     bool
}

// FleetOverview renders the spanner-wide aggregate landing page. Registered
// only in spanner mode (h.spannerAdmin != nil), so it never appears or 404s
// on a standalone server. Each summary fans out to every roster bolt and
// folds in this bolt's own — a single-bolt spanner shows its lone partition,
// which IS the fleet, so the page reads correctly from one bolt up.
func (h *Handler) FleetOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var data FleetOverviewData

	data.Convergence = h.scopedConvergence(scopeFleet)
	data.Security, data.SecurityOK = h.scopedSecurity(ctx, scopeFleet)
	data.Risk, data.RiskOK = h.scopedRisk(ctx, scopeFleet)
	data.Compliance, data.ComplianceOK = h.scopedCompliance(ctx, scopeFleet)
	data.Health, data.HealthOK = h.scopedHealth(ctx, scopeFleet)
	data.EnvoyCount = h.fleetWideEnvoyCount()
	if roster, err := h.spannerAdmin.BoltRoster(ctx); err == nil {
		data.BoltCount = len(roster)
	}

	data.PageData = PageData{Title: "Fleet Overview", ActiveNav: "fleet"}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "fleet-overview", data)
}

// The /fleet/<page> handlers are the dedicated federated views (ADR-037
// "D"): each reuses the SAME *ScopedData builder + template as its per-bolt
// page, but resolves scope via fleetScope (federated by default, never the
// cookie) and renders under the Fleet nav group. The cookie-driven scope-tab
// strip is suppressed here (ScopeTabs = nil) — the /fleet section's per-bolt
// drill is query-based (?scope=) and arrives in slice 3 alongside stripping
// the tabs from the local pages. Until then these show the fleet aggregate;
// a hand-entered ?scope=<bolt> already route-to-owners one bolt.

// fleetCommon applies the shared /fleet-section chrome to a built page: the
// Fleet nav highlight, and the fleet scope pinned onto PageData so the page's
// HTMX poll fragments (which carry ?scope={{.Scope}}) stay in the federated
// view rather than reverting to the cookie's bolt. The cookie-driven scope-tab
// strip is suppressed — the /fleet section's query-based drill lands in
// slice 3.
func (h *Handler) fleetCommon(pd *PageData, title string, r *http.Request) {
	scope := h.fleetScope(r)
	*pd = PageData{Title: title, ActiveNav: "fleet"}
	h.setCommonWithRequest(pd, r)
	pd.Scope = scope
	pd.ScopedAway = h.scopedAway(r.Context(), scope)
	pd.ScopeTabs = nil
}

func (h *Handler) FleetSecurity(w http.ResponseWriter, r *http.Request) {
	data := h.securityScopedData(r.Context(), r, h.fleetScope(r))
	data.QueryTimeoutSec = h.securityQueryTimeoutSec()
	h.fleetCommon(&data.PageData, "Security", r)
	h.renderer.Render(w, "security", data)
}

func (h *Handler) FleetRisk(w http.ResponseWriter, r *http.Request) {
	data := h.riskScopedData(r.Context(), r, h.fleetScope(r))
	if level := r.URL.Query().Get("level"); level != "" {
		var filtered []RiskEnvoyDetail
		for _, e := range data.Envoys {
			if e.Level == level {
				filtered = append(filtered, e)
			}
		}
		data.Envoys = filtered
		data.LevelFilter = level
	}
	h.fleetCommon(&data.PageData, "Risk Posture", r)
	h.renderer.Render(w, "risk", data)
}

func (h *Handler) FleetCompliance(w http.ResponseWriter, r *http.Request) {
	data := h.complianceScopedData(r.Context(), r, h.fleetScope(r))
	h.fleetCommon(&data.PageData, "Compliance", r)
	h.renderer.Render(w, "compliance", data)
}

func (h *Handler) FleetRuns(w http.ResponseWriter, r *http.Request) {
	data := h.runsScopedData(r.Context(), r, h.fleetScope(r))
	sortCol := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortDir != "asc" && sortDir != "desc" {
		sortDir = "desc"
	}
	sortRunRows(data.Runs, sortCol, sortDir)
	data.StatusFilter = r.URL.Query().Get("status")
	data.SortColumn = sortCol
	data.SortDir = sortDir
	h.fleetCommon(&data.PageData, "Runs", r)
	h.renderer.Render(w, "runs", data)
}

func (h *Handler) FleetAgentHealth(w http.ResponseWriter, r *http.Request) {
	data := h.agentHealthScopedData(r.Context(), r, h.fleetScope(r))
	h.fleetCommon(&data.PageData, "Agent Health", r)
	h.renderer.Render(w, "agent-health", data)
}

func (h *Handler) FleetEnvoys(w http.ResponseWriter, r *http.Request) {
	data := h.resolveEnvoysData(r.Context(), r, h.fleetScope(r))
	h.fleetCommon(&data.PageData, "Envoys", r)
	h.renderer.Render(w, "envoys", data)
}
