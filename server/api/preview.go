// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/fleet"
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

// RegisterRoutes registers preview endpoints. Preview surfaces the
// per-envoy impact of a module change (which envoys would be
// affected) — fleet.read is the right gate.
func (h *PreviewHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/preview", read(http.HandlerFunc(h.Preview)))
}

// Preview returns the list of envoys that would be affected by changes to a module.
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

	type affectedEnvoy struct {
		Hostname string `json:"hostname"`
		EnvoyID string `json:"envoy_id"`
	}

	// Find all envoys that include this module.
	allEnvoys := h.fleetIndex.AllEnvoys()
	var affected []affectedEnvoy

	for _, ts := range allEnvoys {
		if ts.Revoked {
			continue
		}
		// Resolve config for this envoy and check if it includes the module.
		_, cfg := h.configStore.Match(ts.Hostname)
		if cfg == nil {
			continue
		}
		for _, mod := range cfg.Modules {
			if mod.Name == moduleName {
				affected = append(affected, affectedEnvoy{
					Hostname: ts.Hostname,
					EnvoyID: ts.ID,
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
