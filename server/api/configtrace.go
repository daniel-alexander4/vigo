// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// ConfigTraceHandler serves the config trace REST endpoint.
type ConfigTraceHandler struct {
	configStore *config.ConfigStore
	fleetIndex  *fleet.FleetIndex
}

// NewConfigTraceHandler creates a ConfigTraceHandler. fleetIndex may be nil
// in tests or dev modes without a running fleet; the user-trace endpoint
// returns an empty result in that case.
func NewConfigTraceHandler(configStore *config.ConfigStore, fleetIndex *fleet.FleetIndex) *ConfigTraceHandler {
	return &ConfigTraceHandler{configStore: configStore, fleetIndex: fleetIndex}
}

// RegisterRoutes registers config trace, tree, and search endpoints.
func (h *ConfigTraceHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config/trace", h.HandleTrace)
	mux.HandleFunc("GET /api/v1/config/trace/user", h.HandleTraceUser)
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

// HandleTraceUser indexes by username and returns one entry per
// (enrolled envoy, module) where a type: user resource matches.
func (h *ConfigTraceHandler) HandleTraceUser(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		writeJSONError(w, http.StatusBadRequest, "missing required ?username= parameter")
		return
	}
	if !isValidUsername(username) {
		writeJSONError(w, http.StatusBadRequest, "invalid username")
		return
	}

	var hostnames []string
	if h.fleetIndex != nil {
		for _, e := range h.fleetIndex.AllEnvoys() {
			if e.Revoked || e.Hostname == "" {
				continue
			}
			hostnames = append(hostnames, e.Hostname)
		}
	}

	trace := h.configStore.TraceUser(username, hostnames)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trace)
}

// isValidUsername enforces a conservative charset to block injection
// attempts via the query parameter. Matches the character set accepted
// by POSIX useradd with `.` additionally allowed for domain-style names.
func isValidUsername(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}
