// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"testing"

	"git.alexander4.io/vigo/server/db"
)

// TestUnknownRoleFailsClosed: a role the code does not know gets no
// permissions, neither from DefaultPermissions nor through a stored row
// whose permissions column is empty.
func TestUnknownRoleFailsClosed(t *testing.T) {
	for _, role := range []string{"", "auditor", "superuser"} {
		if got := DefaultPermissions(role); len(got) != 0 {
			t.Errorf("DefaultPermissions(%q) = %v, want none", role, got)
		}
		u := dbToUser(db.User{Role: role})
		for _, p := range AllPermissions {
			if u.HasPermission(p) {
				t.Errorf("role %q row has %q", role, p)
			}
		}
	}
	if got := PermissionsJSON("auditor"); got != "[]" {
		t.Errorf("PermissionsJSON(unknown) = %s, want []", got)
	}
}
