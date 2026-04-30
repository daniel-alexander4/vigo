// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

// userReq stamps a non-admin user (with the given role and the
// role's default permission set) into the request context. Used by
// per-role access matrix tests in api/.
func userReq(r *http.Request, role string) *http.Request {
	user := &vigoauth.User{
		ID:          "test-" + role,
		Username:    role,
		Role:        role,
		Permissions: vigoauth.DefaultPermissions(role),
	}
	return r.WithContext(vigoauth.ContextWithUser(r.Context(), user))
}
