// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import "github.com/charmbracelet/lipgloss"

// Catppuccin Mocha palette — 24-bit RGB.
var (
	ColorRosewater = lipgloss.Color("#f5e0dc")
	ColorFlamingo  = lipgloss.Color("#f2cdcd")
	ColorPink      = lipgloss.Color("#f5c2e7")
	ColorMauve     = lipgloss.Color("#cba6f7")
	ColorRed       = lipgloss.Color("#f38ba8")
	ColorMaroon    = lipgloss.Color("#eba0ac")
	ColorPeach     = lipgloss.Color("#fab387")
	ColorYellow    = lipgloss.Color("#f9e2af")
	ColorGreen     = lipgloss.Color("#a6e3a1")
	ColorTeal      = lipgloss.Color("#94e2d5")
	ColorSky       = lipgloss.Color("#89dceb")
	ColorSapphire  = lipgloss.Color("#74c7ec")
	ColorBlue      = lipgloss.Color("#89b4fa")
	ColorLavender  = lipgloss.Color("#b4befe")
	ColorText      = lipgloss.Color("#cdd6f4")
	ColorSubtext1  = lipgloss.Color("#bac2de")
	ColorSubtext0  = lipgloss.Color("#a6adc8")
	ColorOverlay2  = lipgloss.Color("#9399b2")
	ColorSurface2  = lipgloss.Color("#585b70")
	ColorSurface1  = lipgloss.Color("#45475a")
	ColorSurface0  = lipgloss.Color("#313244")
	ColorBase      = lipgloss.Color("#1e1e2e")
	ColorMantle    = lipgloss.Color("#181825")
	ColorCrust     = lipgloss.Color("#11111b")
)

// Shared styles.
var (
	// Title bar at the top of the screen.
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorBlue).
			PaddingLeft(1)

	// Tab bar at the bottom.
	TabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorBase).
			Background(ColorBlue).
			Padding(0, 1)

	TabInactiveStyle = lipgloss.NewStyle().
				Foreground(ColorSubtext1).
				Padding(0, 1)

	// Status bar (very bottom).
	StatusBarStyle = lipgloss.NewStyle().
			Foreground(ColorSubtext1).
			PaddingLeft(1)

	StatusErrorStyle = lipgloss.NewStyle().
				Foreground(ColorRed)

	// Stat card styles.
	StatLabelStyle = lipgloss.NewStyle().
			Foreground(ColorSubtext1)

	StatValueStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorText)

	StatHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorBlue).
			MarginBottom(1)

	// Table styles.
	TableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorSapphire)

	TableSelectedStyle = lipgloss.NewStyle().
				Background(ColorSurface0).
				Foreground(ColorText)

	// Section header.
	SectionStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorLavender).
			MarginTop(1).
			PaddingLeft(1)

	// Help text.
	HelpKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorYellow)

	HelpDescStyle = lipgloss.NewStyle().
			Foreground(ColorSubtext1)
)

// StatusColor returns the appropriate color for a convergence/risk status.
func StatusColor(status string) lipgloss.Color {
	switch status {
	case "converged", "ok", "active", "online", "low":
		return ColorGreen
	case "relapsed", "warning", "medium", "partial":
		return ColorYellow
	case "diverged", "stale", "high":
		return ColorPeach
	case "failed", "error", "critical", "offline":
		return ColorRed
	default:
		return ColorSubtext1
	}
}

// StyledStatus renders a status string in its appropriate color.
func StyledStatus(status string) string {
	return lipgloss.NewStyle().Foreground(StatusColor(status)).Render(status)
}
