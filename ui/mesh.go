// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"net/http"

	"git.alexander4.io/vigo/server/swarm/lockboxmesh"
	"git.alexander4.io/vigo/server/swarm/longdrawermesh"
)

// MeshRedirect preserves the pre-split `/mesh` URL — redirects to the
// subsystem page an operator was looking for. `?tab=crypt` → lockbox;
// anything else defaults to longdrawer. One-minor-release compatibility
// step; safe to remove once muscle-memory URLs have migrated.
func (h *Handler) MeshRedirect(w http.ResponseWriter, r *http.Request) {
	dest := "/longdrawer"
	if r.URL.Query().Get("tab") == "crypt" {
		dest = "/swarm/lockbox"
	}
	http.Redirect(w, r, dest, http.StatusMovedPermanently)
}

// findSyncUser finds a user summary by username. Used by the
// longdrawer drilldown fragment handler.
func findSyncUser(f *longdrawermesh.Fleet, user string) *longdrawermesh.UserSummary {
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
// lockbox drilldown fragment handler.
func findCryptUser(f *lockboxmesh.Fleet, user string) *lockboxmesh.UserSummary {
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
