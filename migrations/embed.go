// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package migrations embeds the golang-migrate SQL files into the vigosrv
// binary so the migration set travels with the binary. A binary-only deploy
// (e.g. `upgrade.sh server-only`, which hot-swaps the binary but not the image
// filesystem) can never drift from its schema — the failure mode that crashed
// the server when 000018 shipped in the binary but not the container's
// on-disk migrations dir.
//
// The SQLite migrations live at the package root; the (unsupported this
// release) Postgres set lives under postgres/. server.NewServer reads the
// right subtree via golang-migrate's iofs source.
package migrations

import "embed"

//go:embed *.sql postgres/*.sql
var FS embed.FS
