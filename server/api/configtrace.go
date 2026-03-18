// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"

	"github.com/hobnob/hobnob/server/config"
)

// ConfigTraceHandler serves the config trace REST endpoint.
type ConfigTraceHandler struct {
	configStore *config.ConfigStore
}

// NewConfigTraceHandler creates a ConfigTraceHandler.
func NewConfigTraceHandler(configStore *config.ConfigStore) *ConfigTraceHandler {
	return &ConfigTraceHandler{configStore: configStore}
}

// RegisterRoutes registers config trace, tree, and search endpoints.
func (h *ConfigTraceHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config/trace", h.HandleTrace)
	mux.HandleFunc("GET /api/v1/config/tree", h.HandleTree)
	mux.HandleFunc("GET /api/v1/config/search", h.HandleSearch)
}

// HandleTree returns the directory inheritance structure.
func (h *ConfigTraceHandler) HandleTree(w http.ResponseWriter, r *http.Request) {
	tree := h.configStore.Tree()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tree)
}

// HandleSearch finds thrall entries matching the given criteria.
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
func (h *ConfigTraceHandler) HandleTrace(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	if hostname == "" {
		writeJSONError(w, http.StatusBadRequest, "missing required ?hostname= parameter")
		return
	}

	trace := h.configStore.Trace(hostname)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trace)
}
