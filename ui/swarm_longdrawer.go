// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/i18n"
	"git.alexander4.io/vigo/server/swarm/longdrawermesh"
)

// EchoPageData drives `ui/templates/pages/longdrawer.html`. The
// page opens with a health banner + problems table so an on-call
// operator sees "is anything broken" before any totals.
type EchoPageData struct {
	PageData
	Health HealthBanner
	Sync   *longdrawermesh.Fleet
	Error  string
}

// Longdrawer serves `GET /longdrawer`. Admin-only — fleet-wide per-user
// file lists aren't viewer-appropriate.
func (h *Handler) Longdrawer(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	data := EchoPageData{
		PageData: PageData{Title: "Longdrawer", ActiveNav: "longdrawer"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.mesh == nil {
		data.Error = i18n.CatalogT(data.Locale, "health_banner.mesh_unavailable")
		data.Health = finalizeHealthBanner(data.Locale, "Longdrawer", nil, i18n.CatalogT(data.Locale, "health_banner.aggregator_unavailable"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "longdrawer", data)
		return
	}

	fleet, err := h.mesh.SyncSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner(data.Locale, "Longdrawer", nil, i18n.CatalogT(data.Locale, "health_banner.snapshot_failed"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "longdrawer", data)
		return
	}
	data.Sync = fleet
	data.Health = buildEchoHealth(data.Locale, fleet)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "longdrawer", data)
}

// EchoUserDrilldown renders the HTMX fragment showing one user's
// full longdrawer state across the fleet. Admin-only.
func (h *Handler) EchoUserDrilldown(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	user := r.PathValue("user")
	if user == "" {
		http.Error(w, "user required", http.StatusBadRequest)
		return
	}
	if h.mesh == nil {
		http.Error(w, "mesh aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.mesh.SyncSnapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	summary := findSyncUser(fleet, user)
	if summary == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	frag := struct {
		User   *longdrawermesh.UserSummary
		Now    time.Time
		Locale string
	}{User: summary, Now: time.Now(), Locale: h.localeFromRequest(r)}
	h.renderer.RenderFragment(w, "longdrawer", "longdrawer-user-fragment", frag)
}

// buildEchoHealth derives the problems list from the aggregator's
// existing fields: any user with non-empty Divergences is "err"; any
// envoy in "stale" state is "warn". Sorted by severity so err rows
// surface first.
func buildEchoHealth(locale string, f *longdrawermesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner(locale, "Longdrawer", nil, i18n.CatalogT(locale, "health_banner.no_data"))
	}
	var problems []HealthProblem
	for _, u := range f.Users {
		if len(u.Divergences) == 0 {
			continue
		}
		problems = append(problems, HealthProblem{
			Kind:     "divergent",
			Subject:  u.User,
			Detail:   fmt.Sprintf(i18n.CatalogT(locale, "swarm.longdrawer.divergent_detail"), len(u.Divergences), joinFirstN(u.Divergences, 3)),
			Link:     "/longdrawer#user-" + u.User,
			Severity: "err",
		})
	}
	staleSeen := map[string]struct{}{}
	for _, u := range f.Users {
		for _, ep := range u.Envoys {
			if ep.Status != "stale" {
				continue
			}
			if _, ok := staleSeen[ep.Hostname]; ok {
				continue
			}
			staleSeen[ep.Hostname] = struct{}{}
			problems = append(problems, HealthProblem{
				Kind:     "stale",
				Subject:  ep.Hostname,
				Detail:   i18n.CatalogT(locale, "swarm.longdrawer.stale_detail"),
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf(i18n.CatalogT(locale, "swarm.longdrawer.headline"),
		f.Totals.Users, f.Totals.Envoys, f.Totals.Files)
	return finalizeHealthBanner(locale, "Longdrawer", problems, headline)
}

func joinFirstN(ss []string, n int) string {
	if len(ss) <= n {
		return joinCsv(ss)
	}
	return joinCsv(ss[:n]) + ", …"
}

func joinCsv(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
