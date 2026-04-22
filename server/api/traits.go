// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

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
func (h *TraitsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/envoys/{hostname}/traits", h.HandleGetTraits)
}

// traitsResponse is the JSON envelope returned by the traits endpoint.
type traitsResponse struct {
	Hostname    string          `json:"hostname"`
	CollectedAt string          `json:"collected_at"`
	Traits       json.RawMessage `json:"traits"`
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
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("envoy '%s' not found", hostname))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	traits, err := h.queries.GetLatestEnvoyTraits(ctx, envoy.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound,
				fmt.Sprintf("no traits available for '%s' -- envoy has not reported traits yet", hostname))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := traitsResponse{
		Hostname:    hostname,
		CollectedAt: traits.CollectedAt.Format(time.RFC3339),
		Traits:       json.RawMessage(traits.Traits),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// writeJSONError writes a JSON error response with the given status code.
// Uses {"message": "..."} to match grpc-gateway's error format.
func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"message": message})
}
