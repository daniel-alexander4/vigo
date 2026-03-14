// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"

	"github.com/hobnob/hobnob/server/license"
)

// LicenseHandler serves the license status endpoint for hobnobcli.
type LicenseHandler struct {
	enforcer *license.Enforcer
}

// NewLicenseHandler creates a handler backed by the given enforcer.
func NewLicenseHandler(enforcer *license.Enforcer) *LicenseHandler {
	return &LicenseHandler{enforcer: enforcer}
}

// RegisterRoutes mounts the license endpoints on the given mux.
func (h *LicenseHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/license", h.handleStatus)
}

func (h *LicenseHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := h.enforcer.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
