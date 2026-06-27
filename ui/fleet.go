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
