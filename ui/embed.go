// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package ui provides embedded web UI assets and rendering for the Hobnob dashboard.
package ui

import "embed"

//go:embed templates static
var Assets embed.FS
