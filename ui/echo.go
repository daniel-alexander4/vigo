// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/echomesh"
)

// EchoPageData drives `ui/templates/pages/echo.html`. The
// page opens with a health banner + problems table so an on-call
// operator sees "is anything broken" before any totals.
type EchoPageData struct {
	PageData
	Health HealthBanner
	Sync   *echomesh.Fleet
	Error  string
}

// Echo serves `GET /echo`. Admin-only — fleet-wide per-user
// file lists aren't viewer-appropriate.
func (h *Handler) Echo(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	data := EchoPageData{
		PageData: PageData{Title: "Echo", ActiveNav: "echo"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.mesh == nil {
		data.Error = "mesh aggregator not available"
		data.Health = finalizeHealthBanner("Echo", nil, "Aggregator unavailable")
		data.Health.Level = "warn"
		h.renderer.Render(w, "echo", data)
		return
	}

	fleet, err := h.mesh.SyncSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner("Echo", nil, "Snapshot failed")
		data.Health.Level = "warn"
		h.renderer.Render(w, "echo", data)
		return
	}
	data.Sync = fleet
	data.Health = buildEchoHealth(fleet)
	h.renderer.Render(w, "echo", data)
}

// EchoUserDrilldown renders the HTMX fragment showing one user's
// full echo state across the fleet. Admin-only.
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
		User *echomesh.UserSummary
		Now  time.Time
	}{User: summary, Now: time.Now()}
	h.renderer.RenderFragment(w, "echo", "echo-user-fragment", frag)
}

// buildEchoHealth derives the problems list from the aggregator's
// existing fields: any user with non-empty Divergences is "err"; any
// envoy in "stale" state is "warn". Sorted by severity so err rows
// surface first.
func buildEchoHealth(f *echomesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner("Echo", nil, "No data")
	}
	var problems []HealthProblem
	for _, u := range f.Users {
		if len(u.Divergences) == 0 {
			continue
		}
		problems = append(problems, HealthProblem{
			Kind:     "divergent",
			Subject:  u.User,
			Detail:   fmt.Sprintf("%d file(s) disagree across envoys: %s", len(u.Divergences), joinFirstN(u.Divergences, 3)),
			Link:     "/echo#user-" + u.User,
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
				Detail:   "echo trait not refreshed within the last 5 minutes",
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf("%d user(s), %d envoy(s), %d file(s). No divergences, no stale envoys.",
		f.Totals.Users, f.Totals.Envoys, f.Totals.Files)
	return finalizeHealthBanner("Echo", problems, headline)
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
