// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"

	"git.alexander4.io/vigo/server/auth"
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
// All four are read-only views of the loaded configuration tree
// (per-envoy resolution, directory inheritance, search across
// modules, and module-deduplication preview) — fleet.read is the
// right gate.
func (h *ConfigTraceHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/config/trace", read(http.HandlerFunc(h.HandleTrace)))
	mux.Handle("GET /api/v1/config/tree", read(http.HandlerFunc(h.HandleTree)))
	mux.Handle("GET /api/v1/config/search", read(http.HandlerFunc(h.HandleSearch)))
	mux.Handle("GET /api/v1/config/dedup", read(http.HandlerFunc(h.HandleDedup)))
}

// HandleTree returns the directory inheritance structure.
func (h *ConfigTraceHandler) HandleTree(w http.ResponseWriter, r *http.Request) {
	tree := h.configStore.Tree()
	writeJSON(w, r, http.StatusOK, tree)
}

// HandleSearch finds envoy entries matching the given criteria.
func (h *ConfigTraceHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("match")
	module := r.URL.Query().Get("configcrate")
	varName := r.URL.Query().Get("var")

	if query == "" && module == "" && varName == "" {
		writeError(w, http.StatusBadRequest, "at least one of ?match=, ?configcrate=, or ?var= is required")
		return
	}

	result := h.configStore.Search(query, module, varName)
	writeJSON(w, r, http.StatusOK, result)
}

// HandleTrace serves both forward (host-rooted) and inverse (subject-rooted)
// config trace queries.
//
//   - ?hostname=H            → forward trace for H. ?configcrate=M filters the
//     output to M's branch (existing behavior).
//   - ?configcrate=M              → inverse: enrolled envoys whose resolved config
//     includes module M, plus the carrier that pulled
//     it in.
//   - ?usercrate=U           → inverse: same for usercrate U.
//   - ?include_tree=true     → inverse only: additionally lists envoyEntries
//     patterns that reference the subject but match
//     no enrolled envoy.
//
// Exactly one subject must be specified (?hostname, ?module, or ?usercrate);
// ?module+?usercrate combined is rejected since they each select a different
// inverse subject.
func (h *ConfigTraceHandler) HandleTrace(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hostname := q.Get("hostname")
	module := q.Get("configcrate")
	usercrate := q.Get("usercrate")

	if hostname == "" {
		// Inverse mode — module XOR usercrate.
		if module == "" && usercrate == "" {
			writeError(w, http.StatusBadRequest, "specify one of ?hostname=, ?configcrate=, or ?usercrate=")
			return
		}
		if module != "" && usercrate != "" {
			writeError(w, http.StatusBadRequest, "?configcrate= and ?usercrate= are mutually exclusive")
			return
		}
		subject, value := "configcrate", module
		if usercrate != "" {
			subject, value = "usercrate", usercrate
		}
		includeTree := q.Get("include_tree") == "true"

		var envoys []config.InverseEnvoyRef
		if h.fleetIndex != nil {
			for _, e := range h.fleetIndex.AllEnvoys() {
				if e.Revoked {
					continue
				}
				envoys = append(envoys, config.InverseEnvoyRef{ID: e.ID, Hostname: e.Hostname})
			}
		}

		result := h.configStore.TraceInverse(subject, value, includeTree, envoys)
		writeJSON(w, r, http.StatusOK, result)
		return
	}

	// Forward mode.
	trace := h.configStore.Trace(hostname)
	if module != "" {
		trace = trace.FilterByModule(module)
	}

	writeJSON(w, r, http.StatusOK, trace)
}

// HandleDedup scans the config tree for redundant modules and variables.
func (h *ConfigTraceHandler) HandleDedup(w http.ResponseWriter, r *http.Request) {
	result := h.configStore.Dedup()
	writeJSON(w, r, http.StatusOK, result)
}
