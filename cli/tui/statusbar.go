// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// viewTab describes a top-level view for the tab bar.
type viewTab struct {
	key  string // "1", "2", etc.
	name string
	id   viewID
}

var tabs = []viewTab{
	{"1", "Dashboard", viewDashboard},
	{"2", "Envoys", viewEnvoys},
	{"3", "Runs", viewRuns},
	{"4", "Risk", viewRisk},
	{"5", "Audit", viewAudit},
}

// renderTabBar renders the tab bar with the active view highlighted.
func renderTabBar(active viewID, width int) string {
	var parts []string
	for _, t := range tabs {
		label := fmt.Sprintf("[%s] %s", t.key, t.name)
		if t.id == active {
			parts = append(parts, TabActiveStyle.Render(label))
		} else {
			parts = append(parts, TabInactiveStyle.Render(label))
		}
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	return lipgloss.NewStyle().Width(width).Render(bar)
}

// renderStatusBar renders the bottom status bar.
func renderStatusBar(lastRefresh time.Time, interval time.Duration, err error, width int) string {
	left := StatusBarStyle.Render(fmt.Sprintf("Last refresh: %s", lastRefresh.Format("15:04:05")))

	var right string
	if err != nil {
		right = StatusErrorStyle.Render(fmt.Sprintf("Error: %s", truncate(err.Error(), 60)))
	} else if interval > 0 {
		next := time.Until(lastRefresh.Add(interval))
		if next < 0 {
			next = 0
		}
		right = StatusBarStyle.Render(fmt.Sprintf("Next: %ds  r refresh  ? help  q quit", int(next.Seconds())))
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	spacer := lipgloss.NewStyle().Width(gap).Render("")
	return lipgloss.JoinHorizontal(lipgloss.Top, left, spacer, right)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
