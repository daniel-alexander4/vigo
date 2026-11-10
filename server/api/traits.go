// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// TraitsHandler serves the REST endpoint for envoy trait inspection.
type TraitsHandler struct {
	queries *db.Queries
}

// NewTraitsHandler creates a TraitsHandler using the given database queries.
func NewTraitsHandler(queries *db.Queries) *TraitsHandler {
	return &TraitsHandler{queries: queries}
}

// RegisterRoutes registers the traits endpoint on the given mux.
// Trait blob is per-envoy state — fleet.read.
func (h *TraitsHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("GET /api/v1/envoys/{hostname}/traits", read(http.HandlerFunc(h.HandleGetTraits)))
}

// traitsResponse is the JSON envelope returned by the traits endpoint.
type traitsResponse struct {
	Hostname    string          `json:"hostname"`
	CollectedAt string          `json:"collected_at"`
	Traits      json.RawMessage `json:"traits"`
}

// HandleGetTraits returns the latest collected traits for a envoy, looked up
// by hostname. Returns 404 if the envoy doesn't exist or hasn't reported
// traits yet.
func (h *TraitsHandler) HandleGetTraits(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	ctx := r.Context()

	envoy, err := h.queries.GetEnvoyByHostname(ctx, hostname)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("envoy '%s' not found", hostname))
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	traits, err := h.queries.GetLatestEnvoyTraits(ctx, envoy.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound,
				fmt.Sprintf("no traits available for '%s' -- envoy has not reported traits yet", hostname))
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := traitsResponse{
		Hostname:    hostname,
		CollectedAt: traits.CollectedAt.Format(time.RFC3339),
		Traits:      json.RawMessage(traits.Traits),
	}

	writeJSON(w, r, http.StatusOK, resp)
}
