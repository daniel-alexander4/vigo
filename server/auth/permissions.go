// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"net/http"
)

// Permission constants for fine-grained RBAC.
const (
	PermFleetRead          = "fleet.read"
	PermFleetWrite         = "fleet.write"
	PermConfigPublish      = "config.publish"
	PermConfigRollback     = "config.rollback"
	PermPushExecute        = "push.execute"
	PermQueryExecute       = "query.execute"
	PermWorkflowExecute    = "workflow.execute"
	PermUsersManage        = "users.manage"
	PermTokensManage       = "tokens.manage"
	PermBackupManage       = "backup.manage"
	PermEnrollManage       = "enrollment.manage"
	PermAuditRead          = "audit.read"
	PermComplianceRead     = "compliance.read"
	PermComplianceDocsWrite = "compliance.docs.write"
	PermComplianceDocsPurge = "compliance.docs.purge"
)

// AllPermissions is the complete set of permissions (granted to admins).
var AllPermissions = []string{
	PermFleetRead, PermFleetWrite,
	PermConfigPublish, PermConfigRollback,
	PermPushExecute, PermQueryExecute, PermWorkflowExecute,
	PermUsersManage, PermTokensManage,
	PermBackupManage, PermEnrollManage, PermAuditRead,
	PermComplianceRead, PermComplianceDocsWrite, PermComplianceDocsPurge,
}

// ViewerPermissions is the default set for the viewer role.
var ViewerPermissions = []string{
	PermFleetRead, PermAuditRead, PermComplianceRead,
}

// CompliancePermissions is the default set for the compliance role —
// GRC staff and compliance-document uploaders. Deliberately excludes
// PermAuditRead (the raw audit-log chain stays admin-only). The
// compliance role can download the audit-bundle (gated on
// PermComplianceRead), but the bundle's raw audit-log.csv is itself
// PermAuditRead-gated, so the compliance role never receives the
// hash-chained log through it.
var CompliancePermissions = []string{
	PermComplianceRead, PermComplianceDocsWrite,
}

// ValidRoles enumerates every role string the system accepts. Used by
// CLI / API input validation; the DB CHECK in migrations/000001_*.sql
// must agree.
var ValidRoles = []string{"admin", "viewer", "compliance"}

// IsValidRole reports whether role is in ValidRoles.
func IsValidRole(role string) bool {
	for _, r := range ValidRoles {
		if r == role {
			return true
		}
	}
	return false
}

// DefaultPermissions returns the default permission set for a role.
func DefaultPermissions(role string) []string {
	switch role {
	case "admin":
		return AllPermissions
	case "compliance":
		return CompliancePermissions
	default:
		return ViewerPermissions
	}
}

// PermissionsJSON returns the JSON-encoded permissions for a role.
func PermissionsJSON(role string) string {
	data, _ := json.Marshal(DefaultPermissions(role))
	return string(data)
}

// HasPermission returns true if the user has the given permission.
// Admins always have all permissions regardless of the stored list.
func (u *User) HasPermission(perm string) bool {
	if u == nil {
		return false
	}
	if u.Role == "admin" {
		return true
	}
	for _, p := range u.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// RequirePermission returns middleware that blocks users without the given
// permission. Returns 403 Forbidden with a descriptive error.
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFromContext(r.Context())
			if u == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
				return
			}
			if !u.HasPermission(perm) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{
					"error":      "insufficient permissions",
					"required":   perm,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
