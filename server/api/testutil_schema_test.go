// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"testing"

	"github.com/daniel-alexander4/vigo/server/testutil"
)

// openMigratedTestDB returns an in-memory SQLite database with the full
// migration schema applied. Delegates to the shared testutil package.
func openMigratedTestDB(t *testing.T) *sql.DB {
	return testutil.OpenMigratedTestDB(t)
}
