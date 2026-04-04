// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import "github.com/charmbracelet/lipgloss"

var helpBindings = []struct{ key, desc string }{
	{"1-5", "Switch view"},
	{"Tab", "Next view"},
	{"j/k or arrows", "Navigate list"},
	{"Enter", "Open detail"},
	{"Esc", "Go back"},
	{"/", "Search/filter"},
	{"r", "Refresh now"},
	{"?", "Toggle help"},
	{"q", "Quit"},
}

// renderHelp renders a centered help overlay.
func renderHelp(width, height int) string {
	var rows string
	for _, b := range helpBindings {
		rows += HelpKeyStyle.Render(b.key) + "  " + HelpDescStyle.Render(b.desc) + "\n"
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorSurface2).
		Padding(1, 3).
		Render("Keyboard Shortcuts\n\n" + rows)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
