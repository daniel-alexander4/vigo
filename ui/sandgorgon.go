// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"

	"git.alexander4.io/vigo/server/db"
)

// SandgorgonPageData provides data for the sandgorgon dashboard.
type SandgorgonPageData struct {
	PageData
	TotalAssets      int
	Available        int
	Commissioned     int
	Retired          int
	CertificateCount int
	Assets           []db.SandgorgonAsset
	StateFilter      string
	Search           string
}

// SandgorgonAssetPageData provides data for a single asset detail page.
type SandgorgonAssetPageData struct {
	PageData
	Asset        db.SandgorgonAsset
	Certificates []db.SandgorgonCertificate
	Events       []db.SandgorgonEvent
}

// SandgorgonCertificatesPageData provides data for the certificates list page.
type SandgorgonCertificatesPageData struct {
	PageData
	Certificates []db.SandgorgonCertificate
}

// Sandgorgon renders the bare metal lifecycle dashboard.
func (h *Handler) Sandgorgon(w http.ResponseWriter, r *http.Request) {
	stateFilter := r.URL.Query().Get("state")

	// Get all assets for stat counts.
	allAssets, err := h.queries.ListSandgorgonAssets(r.Context())
	if err != nil {
		allAssets = nil
	}

	// Get filtered assets for the table.
	var displayAssets []db.SandgorgonAsset
	if stateFilter != "" {
		displayAssets, _ = h.queries.ListSandgorgonAssetsByState(r.Context(), stateFilter)
	} else {
		displayAssets = allAssets
	}

	certs, err := h.queries.ListSandgorgonCertificates(r.Context())
	if err != nil {
		certs = nil
	}

	data := SandgorgonPageData{
		PageData:         PageData{Title: "Sandgorgon", ActiveNav: "sandgorgon"},
		TotalAssets:      len(allAssets),
		CertificateCount: len(certs),
		Assets:           displayAssets,
		StateFilter:      stateFilter,
	}

	for _, a := range allAssets {
		switch a.LifecycleState {
		case "available":
			data.Available++
		case "commissioned", "active", "provisioned":
			data.Commissioned++
		case "retired":
			data.Retired++
		}
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "sandgorgon", data)
}

// SandgorgonAssetsFragment serves the HTMX asset table fragment for search.
func (h *Handler) SandgorgonAssetsFragment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	stateFilter := r.URL.Query().Get("state")

	var assets []db.SandgorgonAsset
	if q != "" {
		pattern := "%" + q + "%"
		assets, _ = h.queries.SearchSandgorgonAssets(r.Context(), pattern)
		// Apply state filter on top of search.
		if stateFilter != "" {
			var filtered []db.SandgorgonAsset
			for _, a := range assets {
				if a.LifecycleState == stateFilter {
					filtered = append(filtered, a)
				}
			}
			assets = filtered
		}
	} else if stateFilter != "" {
		assets, _ = h.queries.ListSandgorgonAssetsByState(r.Context(), stateFilter)
	} else {
		assets, _ = h.queries.ListSandgorgonAssets(r.Context())
	}

	data := SandgorgonPageData{
		Assets:      assets,
		StateFilter: stateFilter,
		Search:      q,
	}
	data.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "sandgorgon", "sandgorgon-assets-fragment", data)
}

// SandgorgonAsset renders the detail page for a single hardware asset.
func (h *Handler) SandgorgonAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := h.queries.GetSandgorgonAsset(r.Context(), id)
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}

	certs, _ := h.queries.ListSandgorgonCertificatesByAsset(r.Context(), id)
	events, _ := h.queries.ListSandgorgonEventsByAsset(r.Context(), db.ListSandgorgonEventsByAssetParams{
		AssetID: id,
		Limit:   50,
	})

	hostname := asset.Hostname
	if hostname == "" {
		hostname = "Asset " + id[:8]
	}

	data := SandgorgonAssetPageData{
		PageData:     PageData{Title: hostname + " -- Sandgorgon", ActiveNav: "sandgorgon"},
		Asset:        asset,
		Certificates: certs,
		Events:       events,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "sandgorgon-asset", data)
}

// SandgorgonCertificates renders the NIST 800-88 certificates list page.
func (h *Handler) SandgorgonCertificates(w http.ResponseWriter, r *http.Request) {
	certs, err := h.queries.ListSandgorgonCertificates(r.Context())
	if err != nil {
		certs = nil
	}

	data := SandgorgonCertificatesPageData{
		PageData:     PageData{Title: "NIST 800-88 Certificates", ActiveNav: "sandgorgon-certs"},
		Certificates: certs,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "sandgorgon-certificates", data)
}
