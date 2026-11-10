// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import _ "embed"

// ExampleServerConfig contains the annotated server.yaml reference,
// embedded into the binary at build time. Edit server.yaml.example
// (next to this file) directly — there is no source elsewhere to copy
// from. Consumed by `vigocli server server.yaml full` / `list`.
//
//go:embed server.yaml.example
var ExampleServerConfig string
