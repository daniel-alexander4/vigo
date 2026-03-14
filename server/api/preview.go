// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/fleet"
)

// PreviewHandler provides multi-node impact analysis for config changes.
type PreviewHandler struct {
	configStore *config.ConfigStore
	fleetIndex  *fleet.FleetIndex
}

// NewPreviewHandler creates a PreviewHandler.
func NewPreviewHandler(cs *config.ConfigStore, fi *fleet.FleetIndex) *PreviewHandler {
	return &PreviewHandler{configStore: cs, fleetIndex: fi}
}

// RegisterRoutes registers preview endpoints.
func (h *PreviewHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/preview", h.Preview)
}

// Preview returns the list of thralls that would be affected by changes to a module.
func (h *PreviewHandler) Preview(w http.ResponseWriter, r *http.Request) {
	moduleName := r.URL.Query().Get("module")
	if moduleName == "" {
		writeJSONError(w, http.StatusBadRequest, "module parameter is required")
		return
	}

	if h.fleetIndex == nil {
		writeJSONError(w, http.StatusNotImplemented, "multi-node preview requires FleetIndex")
		return
	}

	type affectedThrall struct {
		Hostname string `json:"hostname"`
		ThrallID string `json:"thrall_id"`
	}

	// Find all thralls that include this module.
	allThralls := h.fleetIndex.AllThralls()
	var affected []affectedThrall

	for _, ts := range allThralls {
		if ts.Revoked {
			continue
		}
		// Resolve config for this thrall and check if it includes the module.
		_, cfg := h.configStore.Match(ts.Hostname)
		if cfg == nil {
			continue
		}
		for _, mod := range cfg.Modules {
			if mod.Name == moduleName {
				affected = append(affected, affectedThrall{
					Hostname: ts.Hostname,
					ThrallID: ts.ID,
				})
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"module":   moduleName,
		"affected": affected,
		"count":    len(affected),
	})
}
