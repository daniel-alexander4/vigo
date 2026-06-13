// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/swarm/lockboxmesh"
)

// LockboxPageData drives `ui/templates/pages/lockbox.html`. Same
// engineer-first shape as longdrawer: health banner + problems first.
type LockboxPageData struct {
	PageData
	Health HealthBanner
	Crypt  *lockboxmesh.Fleet
	Error  string
}

// Lockbox serves `GET /lockbox`. Admin-only.
func (h *Handler) Lockbox(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	data := LockboxPageData{
		PageData: PageData{Title: "Lockbox", ActiveNav: "lockbox"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.mesh == nil {
		data.Error = "mesh aggregator not available"
		data.Health = finalizeHealthBanner("Lockbox", nil, "Aggregator unavailable")
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "lockbox", data)
		return
	}

	fleet, err := h.mesh.CryptSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner("Lockbox", nil, "Snapshot failed")
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "lockbox", data)
		return
	}
	data.Crypt = fleet
	data.Health = buildLockboxHealth(fleet)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "lockbox", data)
}

// LockboxUserDrilldown renders the HTMX fragment for one user.
func (h *Handler) LockboxUserDrilldown(w http.ResponseWriter, r *http.Request) {
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
		User   *lockboxmesh.UserSummary
		Now    time.Time
		Locale string
	}{User: summary, Now: time.Now(), Locale: h.localeFromRequest(r)}
	h.renderer.RenderFragment(w, "lockbox", "lockbox-user-fragment", frag)
}

// buildLockboxHealth — divergences = err, recipient drift = warn,
// stale envoys = warn. The unlocked-sessions tile on the page stays
// informational (an unlocked session isn't a problem by itself).
func buildLockboxHealth(f *lockboxmesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner("Lockbox", nil, "No data")
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
		if u.FailedFiles > 0 {
			problems = append(problems, HealthProblem{
				Kind:     "failed",
				Subject:  u.User,
				Detail:   fmt.Sprintf("%d file(s) failed to encrypt and sit in plaintext, never syncing — check `journalctl -u vigo-envoy | grep lockbox`", u.FailedFiles),
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
				Detail:   "lockbox trait not refreshed within the last 5 minutes",
				Severity: "warn",
			})
		}
	}
	headline := fmt.Sprintf("%d user(s), %d envoy(s), %d file(s). No divergences, no drift, no stale envoys.",
		f.Totals.Users, f.Totals.Envoys, f.Totals.Files)
	return finalizeHealthBanner("Lockbox", problems, headline)
}
