// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRoleDefaults pins the per-role default permission set. Adding a
// new permission to AllPermissions does not automatically grant it to
// the viewer or compliance role — those sets are explicit.
func TestRoleDefaults(t *testing.T) {
	cases := []struct {
		role        string
		mustHave    []string
		mustNotHave []string
	}{
		{
			role:     "admin",
			mustHave: AllPermissions,
		},
		{
			role:     "viewer",
			mustHave: []string{PermFleetRead, PermAuditRead, PermComplianceRead},
			mustNotHave: []string{
				PermFleetWrite, PermConfigPublish, PermPushExecute,
				PermUsersManage, PermComplianceDocsWrite, PermComplianceDocsPurge,
			},
		},
		{
			role:     "compliance",
			mustHave: []string{PermComplianceRead, PermComplianceDocsWrite},
			mustNotHave: []string{
				// Compliance role specifically must not have audit.read —
				// the raw audit log stays admin-only. Verifies the
				// scrutinize-decision constraint.
				PermAuditRead, PermFleetRead, PermFleetWrite,
				PermConfigPublish, PermPushExecute, PermUsersManage,
				PermComplianceDocsPurge,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			perms := DefaultPermissions(tc.role)
			set := make(map[string]bool, len(perms))
			for _, p := range perms {
				set[p] = true
			}
			for _, p := range tc.mustHave {
				if tc.role == "admin" {
					// Admins get every permission via HasPermission's
					// fast path; the slice itself just needs to enumerate
					// AllPermissions for serialization.
					if !set[p] {
						t.Errorf("admin missing %q", p)
					}
					continue
				}
				if !set[p] {
					t.Errorf("role %q missing required %q", tc.role, p)
				}
			}
			for _, p := range tc.mustNotHave {
				if set[p] {
					t.Errorf("role %q has forbidden %q", tc.role, p)
				}
			}
		})
	}
}

// TestComplianceRoleNoAuditRead is the explicit guard for the
// scrutinize constraint: the compliance role must never get
// audit.read, because the raw hash-chained audit log is admin-only.
// Audit *bundle* downloads (compliance evidence artifact) are gated
// separately on compliance.read.
func TestComplianceRoleNoAuditRead(t *testing.T) {
	u := &User{Role: "compliance", Permissions: DefaultPermissions("compliance")}
	if u.HasPermission(PermAuditRead) {
		t.Error("compliance role must not have audit.read — scrutinize constraint violated")
	}
	if !u.HasPermission(PermComplianceRead) {
		t.Error("compliance role must have compliance.read")
	}
	if !u.HasPermission(PermComplianceDocsWrite) {
		t.Error("compliance role must have compliance.docs.write")
	}
}

// TestRequirePermissionMatrix checks every role against
// RequirePermission for a representative permission set. Catches a
// regression where a role's default permission slice diverges from
// what HasPermission returns.
func TestRequirePermissionMatrix(t *testing.T) {
	cases := []struct {
		role  string
		perm  string
		allow bool
	}{
		// Admin: everything.
		{"admin", PermFleetRead, true},
		{"admin", PermConfigPublish, true},
		{"admin", PermAuditRead, true},
		{"admin", PermComplianceRead, true},
		{"admin", PermComplianceDocsWrite, true},
		{"admin", PermComplianceDocsPurge, true},

		// Viewer: read-only fleet + audit + compliance.
		{"viewer", PermFleetRead, true},
		{"viewer", PermAuditRead, true},
		{"viewer", PermComplianceRead, true},
		{"viewer", PermFleetWrite, false},
		{"viewer", PermComplianceDocsWrite, false},
		{"viewer", PermConfigPublish, false},

		// Compliance: only compliance.read + compliance.docs.write.
		{"compliance", PermComplianceRead, true},
		{"compliance", PermComplianceDocsWrite, true},
		{"compliance", PermAuditRead, false},
		{"compliance", PermFleetRead, false},
		{"compliance", PermComplianceDocsPurge, false},
		{"compliance", PermConfigPublish, false},
	}

	for _, tc := range cases {
		t.Run(tc.role+"/"+tc.perm, func(t *testing.T) {
			user := &User{Role: tc.role, Permissions: DefaultPermissions(tc.role)}
			mw := RequirePermission(tc.perm)
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			})
			handler := mw(inner)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/", nil)
			ctx := ContextWithUser(req.Context(), user)
			handler.ServeHTTP(rec, req.WithContext(ctx))

			if tc.allow && !called {
				t.Errorf("role %q with %q expected allow, got %d", tc.role, tc.perm, rec.Code)
			}
			if !tc.allow && called {
				t.Errorf("role %q with %q expected deny, got allowed through", tc.role, tc.perm)
			}
		})
	}
}

// TestIsValidRole pins the role set against the DB CHECK constraint
// — see migrations/000001_initial_schema.up.sql role column.
func TestIsValidRole(t *testing.T) {
	for _, valid := range []string{"admin", "viewer", "compliance"} {
		if !IsValidRole(valid) {
			t.Errorf("expected %q to be valid", valid)
		}
	}
	for _, invalid := range []string{"", "ADMIN", "auditor", "superuser", "root"} {
		if IsValidRole(invalid) {
			t.Errorf("expected %q to be invalid", invalid)
		}
	}
}
