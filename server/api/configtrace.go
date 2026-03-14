// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

// RegisterRoutes registers the config trace endpoint.
func (h *ConfigTraceHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config/trace", h.HandleTrace)
}

// HandleTrace returns detailed config resolution info for a hostname.
func (h *ConfigTraceHandler) HandleTrace(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	if hostname == "" {
		http.Error(w, "missing required ?hostname= parameter", http.StatusBadRequest)
		return
	}

	trace := h.configStore.Trace(hostname)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trace)
}
