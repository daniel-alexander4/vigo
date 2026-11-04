// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package auth

import "os"

// fileOwnerUID cannot answer off unix; callers treat that as a refusal.
// vigosrv ships for Linux only.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }
