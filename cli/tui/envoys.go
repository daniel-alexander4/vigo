// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type envoysModel struct {
	envoys   []Envoy
	ds       DataSource
	cursor   int
	filter   string
	filtered []Envoy
	editing  bool // true when typing in the search bar
	width    int
	height   int
	ready    bool
}

type envoysFetchedMsg struct {
	envoys []Envoy
	err    error
}

func newEnvoysModel(ds DataSource) envoysModel {
	return envoysModel{ds: ds}
}

func (m envoysModel) Init() tea.Cmd {
	return m.fetch()
}

func (m envoysModel) fetch() tea.Cmd {
	return func() tea.Msg {
		envoys, err := m.ds.FetchEnvoys()
		return envoysFetchedMsg{envoys: envoys, err: err}
	}
}

func (m *envoysModel) applyFilter() {
	if m.filter == "" {
		m.filtered = m.envoys
		return
	}
	f := strings.ToLower(m.filter)
	m.filtered = nil
	for _, e := range m.envoys {
		if strings.Contains(strings.ToLower(e.Hostname), f) ||
			strings.Contains(strings.ToLower(e.IPAddress), f) {
			m.filtered = append(m.filtered, e)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m envoysModel) Update(msg tea.Msg) (envoysModel, tea.Cmd) {
	switch msg := msg.(type) {
	case envoysFetchedMsg:
		if msg.err == nil {
			m.envoys = msg.envoys
			m.applyFilter()
		}
		m.ready = true
		return m, nil
	case tea.KeyMsg:
		if m.editing {
			switch msg.String() {
			case "enter", "esc":
				m.editing = false
			case "backspace":
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
					m.applyFilter()
				}
			default:
				if len(msg.String()) == 1 {
					m.filter += msg.String()
					m.applyFilter()
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "/":
			m.editing = true
		}
	}
	return m, nil
}

// SelectedEnvoy returns the currently selected envoy, or nil.
func (m envoysModel) SelectedEnvoy() *Envoy {
	if m.cursor >= 0 && m.cursor < len(m.filtered) {
		e := m.filtered[m.cursor]
		return &e
	}
	return nil
}

func (m envoysModel) View() string {
	if !m.ready {
		return "\n  Loading envoys..."
	}

	var b strings.Builder

	// Search bar.
	if m.editing {
		b.WriteString(fmt.Sprintf("  Search: %s_\n\n", m.filter))
	} else if m.filter != "" {
		b.WriteString(fmt.Sprintf("  Filter: %s (/ to edit, Esc to clear)\n\n", m.filter))
	}

	if len(m.filtered) == 0 {
		b.WriteString("  No envoys found.\n")
		return b.String()
	}

	// Header.
	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("HOSTNAME", 24)),
		TableHeaderStyle.Render(padRight("IP", 18)),
		TableHeaderStyle.Render(padRight("LAST SEEN", 20)),
		TableHeaderStyle.Render(padRight("STATUS", 12)),
	))

	// Determine visible window.
	maxVisible := m.height - 8 // leave room for header, tabs, status
	if maxVisible < 3 {
		maxVisible = 3
	}
	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.filtered) {
		end = len(m.filtered)
	}

	for i := start; i < end; i++ {
		e := m.filtered[i]
		status := "active"
		if e.Revoked {
			status = "revoked"
		}

		line := fmt.Sprintf("  %s  %s  %s  %s",
			padRight(e.Hostname, 24),
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(e.IPAddress, 18)),
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(formatTS(e.LastSeen), 20)),
			StyledStatus(status),
		)

		if i == m.cursor {
			line = TableSelectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(fmt.Sprintf("\n  %d envoys", len(m.filtered)))
	if m.filter != "" {
		b.WriteString(fmt.Sprintf(" (filtered from %d)", len(m.envoys)))
	}
	b.WriteString("\n")

	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
