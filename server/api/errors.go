// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
)

// APIError is the canonical JSON shape for every error response from
// the REST API. The field name `message` matches grpc-gateway's error
// envelope, so handlers behind the gateway and pure-REST handlers
// produce indistinguishable error bodies — one parser handles both
// in vigocli and the web UI.
type APIError struct {
	Message string `json:"message"`
}

// writeError emits a JSON-encoded APIError with the given HTTP status.
// Single source of truth for error responses across server/api: every
// handler must use this rather than calling http.Error directly or
// hand-marshaling a different envelope shape.
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	// Status + headers are already committed; if the connection died
	// mid-encode there is nothing meaningful to surface to the caller.
	// best-effort
	_ = json.NewEncoder(w).Encode(APIError{Message: message})
}
