// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package ui provides embedded web UI assets and rendering for the Vigo dashboard.
package ui

import "embed"

//go:embed templates static
var Assets embed.FS
