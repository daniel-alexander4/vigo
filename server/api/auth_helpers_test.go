// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	vigoauth "git.alexander4.io/vigo/server/auth"
)

// adminReq returns a copy of r with a fully-permissioned admin user
// stamped into the context. Tests use this when serving requests
// through a mux that has RequirePermission / RequireAdmin middleware
// — otherwise the gates 401 every test request. Lives in a _test.go
// file so it never enters production builds.
func adminReq(r *http.Request) *http.Request {
	user := &vigoauth.User{
		ID:          "test-admin",
		Username:    "admin",
		Role:        "admin",
		Permissions: vigoauth.AllPermissions,
	}
	return r.WithContext(vigoauth.ContextWithUser(r.Context(), user))
}

// viewerReq returns a copy of r with a viewer user stamped into the
// context — the read-only role, which holds fleet.read but is not
// admin. Tests use this to exercise the non-admin branch of handlers
// that redact admin-only fields.
func viewerReq(r *http.Request) *http.Request {
	user := &vigoauth.User{
		ID:          "test-viewer",
		Username:    "viewer",
		Role:        "viewer",
		Permissions: []string{vigoauth.PermFleetRead},
	}
	return r.WithContext(vigoauth.ContextWithUser(r.Context(), user))
}
