// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/license"
)

// LicenseHandler serves the license status endpoint for vigocli.
type LicenseHandler struct {
	enforcer *license.Enforcer
}

// NewLicenseHandler creates a handler backed by the given enforcer.
func NewLicenseHandler(enforcer *license.Enforcer) *LicenseHandler {
	return &LicenseHandler{enforcer: enforcer}
}

// RegisterRoutes mounts the license endpoints on the given mux.
// License status is fleet metadata — node count, watermark, expiry —
// so fleet.read is the right gate.
func (h *LicenseHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/license", read(http.HandlerFunc(h.handleStatus)))
}

func (h *LicenseHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := h.enforcer.GetStatus()
	// Customer identity and the machine fingerprint are admin-only. Viewers
	// (and compliance) get the operational fields — tier, count, expiry —
	// but not who the license is issued to or its machine binding. Copy
	// before redacting: GetStatus hands back a pointer to the enforcer's
	// live payload, which must not be mutated.
	if status.License != nil && !auth.UserFromContext(r.Context()).IsAdmin() {
		redacted := *status.License
		redacted.CustomerName = ""
		redacted.CustomerID = ""
		redacted.LicenseID = ""
		redacted.Fingerprint = ""
		status.License = &redacted
	}
	writeJSON(w, r, http.StatusOK, status)
}
