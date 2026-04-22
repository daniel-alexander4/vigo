// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/vigocryptmesh"
	"git.alexander4.io/vigo/server/vigosyncmesh"
)

// MeshPageData drives `ui/templates/pages/mesh.html`. The page
// carries both subsystems side-by-side in a tabbed layout; the
// template decides which tab is active based on the `?tab` query
// param. Rendering both aggregates on every request is fine — each
// aggregator caches for 30s behind the scenes.
type MeshPageData struct {
	PageData
	ActiveTab string // "sync" or "crypt"
	Sync      *vigosyncmesh.Fleet
	Crypt     *vigocryptmesh.Fleet
	// Non-nil when the aggregator returned an error or wasn't
	// available at startup. The template renders a banner with the
	// message and otherwise behaves as if the fleet is empty.
	SyncError  string
	CryptError string
}

// Mesh serves `GET /mesh` — the unified vigosync + vigocrypt
// observability page. Admin-only per the existing /api/v1 RBAC;
// viewer-role operators get a 404 from the server's admin
// middleware before reaching this handler.
func (h *Handler) Mesh(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	if tab != "crypt" {
		tab = "sync"
	}

	data := MeshPageData{
		PageData:  PageData{Title: "Sync Mesh", ActiveNav: "mesh"},
		ActiveTab: tab,
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.mesh == nil {
		data.SyncError = "mesh aggregator not available"
		data.CryptError = "mesh aggregator not available"
		h.renderer.Render(w, "mesh", data)
		return
	}

	ctx := r.Context()
	if s, err := h.mesh.SyncSnapshot(ctx); err != nil {
		data.SyncError = err.Error()
	} else {
		data.Sync = s
	}
	if c, err := h.mesh.CryptSnapshot(ctx); err != nil {
		data.CryptError = err.Error()
	} else {
		data.Crypt = c
	}

	h.renderer.Render(w, "mesh", data)
}

// MeshSyncUserDrilldown renders an HTMX fragment showing one
// user's full vigosync state across the fleet — per-file table,
// per-envoy digest column, tombstone list. Fetches fresh; no
// caching beyond the aggregator's own 30s cache.
func (h *Handler) MeshSyncUserDrilldown(w http.ResponseWriter, r *http.Request) {
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
		User *vigosyncmesh.UserSummary
		Now  time.Time
	}{User: summary, Now: time.Now()}
	h.renderer.RenderFragment(w, "mesh", "mesh-sync-user-fragment", frag)
}

// MeshCryptUserDrilldown renders the vigocrypt equivalent.
func (h *Handler) MeshCryptUserDrilldown(w http.ResponseWriter, r *http.Request) {
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
		User *vigocryptmesh.UserSummary
		Now  time.Time
	}{User: summary, Now: time.Now()}
	h.renderer.RenderFragment(w, "mesh", "mesh-crypt-user-fragment", frag)
}

func findSyncUser(f *vigosyncmesh.Fleet, user string) *vigosyncmesh.UserSummary {
	if f == nil {
		return nil
	}
	for i := range f.Users {
		if f.Users[i].User == user {
			return &f.Users[i]
		}
	}
	return nil
}

func findCryptUser(f *vigocryptmesh.Fleet, user string) *vigocryptmesh.UserSummary {
	if f == nil {
		return nil
	}
	for i := range f.Users {
		if f.Users[i].User == user {
			return &f.Users[i]
		}
	}
	return nil
}
