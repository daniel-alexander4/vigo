// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"
	"os"

	"git.alexander4.io/vigo/server/auth"
)

// OpenAPIHandler serves the OpenAPI specification.
type OpenAPIHandler struct {
	specPath string
}

// NewOpenAPIHandler creates a handler that serves the OpenAPI spec from the given path.
func NewOpenAPIHandler(specPath string) *OpenAPIHandler {
	return &OpenAPIHandler{specPath: specPath}
}

// RegisterRoutes registers the OpenAPI endpoint. The spec itself is
// non-sensitive (just the API surface description) but shouldn't
// leak to unauthenticated callers — RequireAuth is the right gate,
// any role can fetch it for tooling.
func (h *OpenAPIHandler) RegisterRoutes(mux *http.ServeMux) {
	authed := auth.RequireAuth("")
	mux.Handle("GET /api/v1/openapi.json", authed(http.HandlerFunc(h.ServeSpec)))
}

// ServeSpec serves the OpenAPI JSON spec.
func (h *OpenAPIHandler) ServeSpec(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(h.specPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "OpenAPI spec not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
