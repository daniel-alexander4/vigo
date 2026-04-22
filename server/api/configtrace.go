// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/fleet"
)

// ConfigTraceHandler serves the config trace REST endpoint.
type ConfigTraceHandler struct {
	configStore *config.ConfigStore
	fleetIndex  *fleet.FleetIndex
}

// NewConfigTraceHandler creates a ConfigTraceHandler. fleetIndex is kept
// on the handler for future per-envoy fleet lookups; it may be nil in
// tests or dev modes without a running fleet.
func NewConfigTraceHandler(configStore *config.ConfigStore, fleetIndex *fleet.FleetIndex) *ConfigTraceHandler {
	return &ConfigTraceHandler{configStore: configStore, fleetIndex: fleetIndex}
}

// RegisterRoutes registers config trace, tree, and search endpoints.
func (h *ConfigTraceHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config/trace", h.HandleTrace)
	mux.HandleFunc("GET /api/v1/config/tree", h.HandleTree)
	mux.HandleFunc("GET /api/v1/config/search", h.HandleSearch)
	mux.HandleFunc("GET /api/v1/config/dedup", h.HandleDedup)
}

// HandleTree returns the directory inheritance structure.
func (h *ConfigTraceHandler) HandleTree(w http.ResponseWriter, r *http.Request) {
	tree := h.configStore.Tree()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tree)
}

// HandleSearch finds envoy entries matching the given criteria.
func (h *ConfigTraceHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("match")
	module := r.URL.Query().Get("module")
	varName := r.URL.Query().Get("var")

	if query == "" && module == "" && varName == "" {
		writeJSONError(w, http.StatusBadRequest, "at least one of ?match=, ?module=, or ?var= is required")
		return
	}

	result := h.configStore.Search(query, module, varName)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleTrace returns detailed config resolution info for a hostname.
// Optional ?module= parameter filters the trace to only show the branch
// relevant to that module.
func (h *ConfigTraceHandler) HandleTrace(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	if hostname == "" {
		writeJSONError(w, http.StatusBadRequest, "missing required ?hostname= parameter")
		return
	}

	trace := h.configStore.Trace(hostname)

	if module := r.URL.Query().Get("module"); module != "" {
		trace = trace.FilterByModule(module)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trace)
}

// HandleDedup scans the config tree for redundant modules and variables.
func (h *ConfigTraceHandler) HandleDedup(w http.ResponseWriter, r *http.Request) {
	result := h.configStore.Dedup()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

