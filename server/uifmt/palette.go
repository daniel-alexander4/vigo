// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package uifmt holds cross-cutting display data shared between the CLI
// and the web UI. Per project convention, this package owns DATA only —
// hex color constants, parsers, classifiers — never rendering. Each
// surface (CLI ANSI, UI HTML/CSS, SVG fills) builds its own renderings
// from these constants in its own package.
package uifmt

// Catppuccin Mocha palette — 24-bit RGB hex constants.
// Canonical color values for both terminal (rebuilt to ANSI in cli/format.go)
// and web (referenced directly in ui/viz_common.go and CSS-as-data).
// Official Catppuccin spec: https://catppuccin.com/palette
const (
	// Accents
	HexRed       = "#f38ba8"
	HexMaroon    = "#eba0ac"
	HexPeach     = "#fab387"
	HexYellow    = "#f9e2af"
	HexGreen     = "#a6e3a1"
	HexTeal      = "#94e2d5"
	HexSky       = "#89dceb"
	HexSapphire  = "#74c7ec"
	HexBlue      = "#89b4fa"
	HexLavender  = "#b4befe"
	HexMauve     = "#cba6f7"
	HexPink      = "#f5c2e7"
	HexFlamingo  = "#f2cdcd"
	HexRosewater = "#f5e0dc"

	// Text + overlays
	HexText     = "#cdd6f4"
	HexSubtext1 = "#bac2de"
	HexSubtext0 = "#a6adc8"
	HexOverlay2 = "#9399b2"
	HexOverlay1 = "#7f849c"
	HexOverlay0 = "#6c7086"

	// Surfaces (backgrounds)
	HexSurface2 = "#585b70"
	HexSurface1 = "#45475a"
	HexSurface0 = "#313244"
	HexBase     = "#1e1e2e"
	HexMantle   = "#181825"
	HexCrust    = "#11111b"
)
