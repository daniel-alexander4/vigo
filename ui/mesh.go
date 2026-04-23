// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"

	"git.alexander4.io/vigo/server/sigilmesh"
	"git.alexander4.io/vigo/server/echomesh"
)

// MeshRedirect preserves the pre-split `/mesh` URL — redirects to the
// subsystem page an operator was looking for. `?tab=crypt` → sigil;
// anything else defaults to echo. One-minor-release compatibility
// step; safe to remove once muscle-memory URLs have migrated.
func (h *Handler) MeshRedirect(w http.ResponseWriter, r *http.Request) {
	dest := "/echo"
	if r.URL.Query().Get("tab") == "crypt" {
		dest = "/sigil"
	}
	http.Redirect(w, r, dest, http.StatusMovedPermanently)
}

// findSyncUser finds a user summary by username. Used by the
// echo drilldown fragment handler.
func findSyncUser(f *echomesh.Fleet, user string) *echomesh.UserSummary {
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

// findCryptUser finds a user summary by username. Used by the
// sigil drilldown fragment handler.
func findCryptUser(f *sigilmesh.Fleet, user string) *sigilmesh.UserSummary {
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
