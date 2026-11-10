// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package auth

import (
	"os"
	"syscall"
)

// fileOwnerUID returns the owning uid of a file from its Lstat info.
func fileOwnerUID(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
