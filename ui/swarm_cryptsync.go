// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/swarm/cryptsyncmesh"
)

// CryptsyncPageData drives `ui/templates/pages/cryptsync.html`. Same
// engineer-first shape as longdrawer: health banner + problems first.
type CryptsyncPageData struct {
	PageData
	Health HealthBanner
	Crypt  *cryptsyncmesh.Fleet
	Error  string
}

// Cryptsync serves `GET /cryptsync`. Admin-only.
func (h *Handler) Cryptsync(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	data := CryptsyncPageData{
		PageData: PageData{Title: "Cryptsync", ActiveNav: "cryptsync"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.mesh == nil {
		data.Error = "mesh aggregator not available"
		data.Health = finalizeHealthBanner("Cryptsync", nil, "Aggregator unavailable")
		data.Health.Level = "warn"
		h.renderer.Render(w, "cryptsync", data)
		return
	}

	fleet, err := h.mesh.CryptSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner("Cryptsync", nil, "Snapshot failed")
		data.Health.Level = "warn"
		h.renderer.Render(w, "cryptsync", data)
		return
	}
	data.Crypt = fleet
	data.Health = buildCryptsyncHealth(fleet)
	h.renderer.Render(w, "cryptsync", data)
}

// CryptsyncUserDrilldown renders the HTMX fragment for one user.
func (h *Handler) CryptsyncUserDrilldown(w http.ResponseWriter, r *http.Request) {
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
	fleet, err := h.mesh.CryptSnapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	summary := findCryptUser(fleet, user)
	if summary == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	frag := struct {
		User *cryptsyncmesh.UserSummary
		Now  time.Time
	}{User: summary, Now: time.Now()}
	h.renderer.RenderFragment(w, "cryptsync", "cryptsync-user-fragment", frag)
}

// buildCryptsyncHealth — divergences = err, recipient drift = warn,
// stale envoys = warn. The unlocked-sessions tile on the page stays
// informational (an unlocked session isn't a problem by itself).
func buildCryptsyncHealth(f *cryptsyncmesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner("Cryptsync", nil, "No data")
	}
	var problems []HealthProblem
	for _, u := range f.Users {
		if len(u.Divergences) > 0 {
			problems = append(problems, HealthProblem{
				Kind:     "divergent",
				Subject:  u.User,
				Detail:   fmt.Sprintf("%d file(s) disagree across envoys: %s", len(u.Divergences), joinFirstN(u.Divergences, 3)),
				Severity: "err",
			})
		}
		if u.RecipientDrift {
			problems = append(problems, HealthProblem{
				Kind:     "drift",
				Subject:  u.User,
				Detail:   "peer set differs across envoys; newly-encrypted files won't decrypt on unknown peers",
				Severity: "warn",
			})
		}
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
				Detail:   "cryptsync trait not refreshed within the last 5 minutes",
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf("%d user(s), %d envoy(s), %d file(s). No divergences, no drift, no stale envoys.",
		f.Totals.Users, f.Totals.Envoys, f.Totals.Files)
	return finalizeHealthBanner("Cryptsync", problems, headline)
}
