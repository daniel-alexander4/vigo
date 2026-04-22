// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http"

	"git.alexander4.io/vigo/server/vigocryptmesh"
	"git.alexander4.io/vigo/server/vigosyncmesh"
)

// MeshRedirect preserves the pre-split `/mesh` URL — redirects to the
// subsystem page an operator was looking for. `?tab=crypt` → vigocrypt;
// anything else defaults to vigosync. One-minor-release compatibility
// step; safe to remove once muscle-memory URLs have migrated.
func (h *Handler) MeshRedirect(w http.ResponseWriter, r *http.Request) {
	dest := "/vigosync"
	if r.URL.Query().Get("tab") == "crypt" {
		dest = "/vigocrypt"
	}
	http.Redirect(w, r, dest, http.StatusMovedPermanently)
}

// findSyncUser finds a user summary by username. Used by the
// vigosync drilldown fragment handler.
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

// findCryptUser finds a user summary by username. Used by the
// vigocrypt drilldown fragment handler.
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
