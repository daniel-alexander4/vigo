// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import _ "embed"

// ExampleServerConfig contains the annotated server.yaml.example file,
// copied from server-seed/ at build time by `make embed-examples`.
//
//go:embed server.yaml.example
var ExampleServerConfig string
