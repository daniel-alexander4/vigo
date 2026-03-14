// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"
	"os"
)

// OpenAPIHandler serves the OpenAPI specification.
type OpenAPIHandler struct {
	specPath string
}

// NewOpenAPIHandler creates a handler that serves the OpenAPI spec from the given path.
func NewOpenAPIHandler(specPath string) *OpenAPIHandler {
	return &OpenAPIHandler{specPath: specPath}
}

// RegisterRoutes registers the OpenAPI endpoint.
func (h *OpenAPIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/openapi.json", h.ServeSpec)
}

// ServeSpec serves the OpenAPI JSON spec.
func (h *OpenAPIHandler) ServeSpec(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(h.specPath)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "OpenAPI spec not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
