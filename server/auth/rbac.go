// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
)

// RequireAdmin returns middleware that blocks non-admin users with 403.
func RequireAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFromContext(r.Context())
			if u == nil || !u.IsAdmin() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "admin role required",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth returns middleware that blocks unauthenticated requests with 401.
func RequireAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFromContext(r.Context())
			if u == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "authentication required",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
