// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"

	"github.com/google/uuid"

	"git.alexander4.io/vigo/server/fleetpressure"
)

// EnvoyFootprintPageData feeds the per-envoy "Agent Footprint" tab.
// Every numeric field flows through the fleetpressure SoT package
// so the dashboard rollup card and this panel can never disagree
// on thresholds — see the envoyStatsSot feedback memory.
type EnvoyFootprintPageData struct {
	PageData
	Hostname string
	EnvoyID  string
	HasData  bool
	Pressure fleetpressure.EnvoyPressure
}

// EnvoyFootprintFragment renders the Agent Footprint tab body for one
// envoy. Surfaces the `agent_self.*` and `host_self.*` traits the
// agent emits every check-in — operator-facing visibility into the
// agent process's own resource usage and the host's FD pressure.
func (h *Handler) EnvoyFootprintFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	pressure, ok := h.dataSource.EnvoyPressure(idStr)
	data := EnvoyFootprintPageData{
		PageData: PageData{Title: "Agent Footprint: " + detail.Envoy.Hostname, ActiveNav: "envoys"},
		Hostname: detail.Envoy.Hostname,
		EnvoyID:  idStr,
		HasData:  ok && pressure.Overall != fleetpressure.Unknown,
		Pressure: pressure,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-footprint-fragment", data)
}

// DashboardPressureFragment renders the "Agent Pressure" stat-card
// column for the dashboard. Polled at the same cadence as the
// surrounding stats fragment (HTMX hx-trigger every PollInterval);
// numbers are pulled fresh from the fleet index on every request.
func (h *Handler) DashboardPressureFragment(w http.ResponseWriter, r *http.Request) {
	summary := h.dataSource.FleetPressure()
	data := struct {
		PageData
		Pressure fleetpressure.FleetSummary
	}{
		Pressure: summary,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.RenderFragment(w, "dashboard", "dashboard-pressure", data)
}
