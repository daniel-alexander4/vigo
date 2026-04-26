// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"

	"git.alexander4.io/vigo/server/swarm/cryptsyncmesh"
	"git.alexander4.io/vigo/server/swarm/filesyncmesh"
)

// MeshRedirect preserves the pre-split `/mesh` URL — redirects to the
// subsystem page an operator was looking for. `?tab=crypt` → cryptsync;
// anything else defaults to filesync. One-minor-release compatibility
// step; safe to remove once muscle-memory URLs have migrated.
func (h *Handler) MeshRedirect(w http.ResponseWriter, r *http.Request) {
	dest := "/filesync"
	if r.URL.Query().Get("tab") == "crypt" {
		dest = "/swarm/cryptsync"
	}
	http.Redirect(w, r, dest, http.StatusMovedPermanently)
}

// findSyncUser finds a user summary by username. Used by the
// filesync drilldown fragment handler.
func findSyncUser(f *filesyncmesh.Fleet, user string) *filesyncmesh.UserSummary {
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
// cryptsync drilldown fragment handler.
func findCryptUser(f *cryptsyncmesh.Fleet, user string) *cryptsyncmesh.UserSummary {
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
