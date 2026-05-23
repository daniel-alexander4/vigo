// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLocalAdminToken is a fixed vgot_-shaped token the auth tests wire into
// NewTokenStore to exercise the local-admin gate that replaced peer auth.
const testLocalAdminToken = "vgot_localadmintokenfixture00000000000000000000000000000000000000000"

func TestLoadOrCreateLocalAdminToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli-admin-token")

	// First call mints the token and persists it.
	tok, err := LoadOrCreateLocalAdminToken(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(tok, apiTokenPrefix), "minted token must carry the vgot_ prefix")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "token file must be 0600 — non-root must not read it")

	// Second call reloads the same token — stable across server restarts.
	again, err := LoadOrCreateLocalAdminToken(path)
	require.NoError(t, err)
	assert.Equal(t, tok, again, "token must be stable across reload")
}
