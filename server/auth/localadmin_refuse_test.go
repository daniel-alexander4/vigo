// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadOrCreateLocalAdminToken_RefusesLooseFile: an existing token file
// another user could read (any group/other bit), a symlink, or one that
// does not hold a vgot_ token is refused rather than trusted.
func TestLoadOrCreateLocalAdminToken_RefusesLooseFile(t *testing.T) {
	dir := t.TempDir()
	good, err := LoadOrCreateLocalAdminToken(filepath.Join(dir, "minted"))
	require.NoError(t, err)

	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		p := filepath.Join(dir, "loose-"+mode.String())
		require.NoError(t, os.WriteFile(p, []byte(good+"\n"), 0o600))
		require.NoError(t, os.Chmod(p, mode))
		_, err := LoadOrCreateLocalAdminToken(p)
		assert.Error(t, err, "mode %04o must be refused", mode)
	}

	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(filepath.Join(dir, "minted"), link))
	_, err = LoadOrCreateLocalAdminToken(link)
	assert.Error(t, err, "a symlinked token file must be refused")

	junk := filepath.Join(dir, "junk")
	require.NoError(t, os.WriteFile(junk, []byte("not-a-token\n"), 0o600))
	_, err = LoadOrCreateLocalAdminToken(junk)
	assert.Error(t, err, "a file without a vgot_ token must be refused")

	tight := filepath.Join(dir, "tight")
	require.NoError(t, os.WriteFile(tight, []byte(good+"\n"), 0o400))
	got, err := LoadOrCreateLocalAdminToken(tight)
	require.NoError(t, err, "0400 owned by us is acceptable")
	assert.Equal(t, good, got)
}
