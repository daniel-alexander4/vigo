// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"net/http"
	"strings"

	"git.alexander4.io/vigo/server/i18n"
	"git.alexander4.io/vigo/server/swarm/puddlemesh"
)

// PuddlePageData drives `ui/templates/pages/puddle.html`. Carries both
// the list view (Fleet.Puddles) and detail view (Puddle) — the template
// renders detail when Puddle is non-nil.
type PuddlePageData struct {
	PageData
	Health HealthBanner
	Fleet  *puddlemesh.Fleet
	Puddle *puddlemesh.PuddleSummary
	Error  string
}

// Puddle serves `GET /swarm/puddle` (list) and
// `GET /swarm/puddle/{pubkey}` (detail). Admin-only — the fleet puddle
// topology exposes per-user identity pubkeys and membership that aren't
// viewer-appropriate. `{pubkey}` accepts the full 64-hex pubkey or an
// uncontested owner name.
func (h *Handler) Puddle(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	if id := r.PathValue("pubkey"); id != "" {
		h.puddleDetail(w, r, id)
		return
	}
	h.puddleList(w, r)
}

func (h *Handler) puddleList(w http.ResponseWriter, r *http.Request) {
	data := PuddlePageData{
		PageData: PageData{Title: "Puddles", ActiveNav: "puddle"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.puddleFleet == nil {
		data.Error = i18n.CatalogT(data.Locale, "health_banner.mesh_unavailable")
		data.Health = finalizeHealthBanner(data.Locale, "Puddles", nil, i18n.CatalogT(data.Locale, "health_banner.aggregator_unavailable"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "puddle", data)
		return
	}
	fleet, err := h.puddleFleet.FleetSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner(data.Locale, "Puddles", nil, i18n.CatalogT(data.Locale, "health_banner.snapshot_failed"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "puddle", data)
		return
	}
	data.Fleet = fleet
	data.Health = buildPuddleHealth(data.Locale, fleet)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "puddle", data)
}

func (h *Handler) puddleDetail(w http.ResponseWriter, r *http.Request, id string) {
	data := PuddlePageData{
		PageData: PageData{Title: "Puddles", ActiveNav: "puddle"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.puddleFleet == nil {
		http.Error(w, "puddle aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	fleet, err := h.puddleFleet.FleetSnapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	// Pubkey (canonical) first, then uncontested owner-name fallback.
	summary := fleet.LookupPuddle(strings.TrimPrefix(id, "ed25519:"))
	if summary == nil {
		summary = fleet.LookupByOwner(id)
	}
	if summary == nil {
		http.NotFound(w, r)
		return
	}
	data.Puddle = summary
	data.Health = buildPuddleSummaryHealth(data.Locale, summary)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "puddle", data)
}

// buildPuddleHealth surfaces every puddle whose health band is not
// healthy as a banner problem, with the underlying reasons.
func buildPuddleHealth(locale string, f *puddlemesh.Fleet) HealthBanner {
	if f == nil {
		return finalizeHealthBanner(locale, "Puddles", nil, i18n.CatalogT(locale, "health_banner.no_data"))
	}
	var problems []HealthProblem
	for i := range f.Puddles {
		p := &f.Puddles[i]
		if p.Health.Band == puddlemesh.HealthHealthy {
			continue
		}
		problems = append(problems, HealthProblem{
			Kind:     p.Health.Band,
			Subject:  puddleSubject(p),
			Detail:   strings.Join(p.Health.Reasons, "; "),
			Link:     "/swarm/puddle/" + p.PuddlePubkey,
			Severity: puddleSeverity(p.Health.Band),
		})
	}
	headline := fmt.Sprintf(
		i18n.CatalogT(locale, "swarm.puddle.headline"),
		f.Totals.Puddles, f.Totals.Envoys, f.Totals.Named, f.Totals.Unlocked, f.Totals.Unhealthy,
	)
	return finalizeHealthBanner(locale, "Puddles", problems, headline)
}

func buildPuddleSummaryHealth(locale string, p *puddlemesh.PuddleSummary) HealthBanner {
	var problems []HealthProblem
	if p.Health.Band != puddlemesh.HealthHealthy {
		problems = append(problems, HealthProblem{
			Kind:     p.Health.Band,
			Subject:  puddleSubject(p),
			Detail:   strings.Join(p.Health.Reasons, "; "),
			Severity: puddleSeverity(p.Health.Band),
		})
	}
	headline := fmt.Sprintf(i18n.CatalogT(locale, "swarm.puddle.summary_headline"), puddleSubject(p), p.EnvoyCount)
	return finalizeHealthBanner(locale, puddleSubject(p), problems, headline)
}

// puddleSubject is the human label for a puddle: its owner name, or a
// short pubkey when unnamed/contested.
func puddleSubject(p *puddlemesh.PuddleSummary) string {
	if p.Owner != "" {
		return p.Owner
	}
	pk := p.PuddlePubkey
	if len(pk) > 16 {
		pk = pk[:16] + "…"
	}
	return pk
}

func puddleSeverity(band string) string {
	if band == puddlemesh.HealthDegraded {
		return "err"
	}
	return "warn"
}
