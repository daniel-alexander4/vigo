// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package auth

import "os"

// fileOwnerUID cannot answer off unix; callers treat that as a refusal.
// vigosrv ships for Linux only.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }
