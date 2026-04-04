// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type runsModel struct {
	runs     []Run
	filtered []Run
	ds       DataSource
	cursor   int
	filter   string
	editing  bool
	width    int
	height   int
	ready    bool
}

type runsFetchedMsg struct {
	runs []Run
	err  error
}

func newRunsModel(ds DataSource) runsModel {
	return runsModel{ds: ds}
}

func (m runsModel) Init() tea.Cmd {
	return m.fetch()
}

func (m runsModel) fetch() tea.Cmd {
	return func() tea.Msg {
		runs, err := m.ds.FetchRuns(50)
		return runsFetchedMsg{runs: runs, err: err}
	}
}

func (m *runsModel) applyFilter() {
	if m.filter == "" {
		m.filtered = m.runs
		return
	}
	f := strings.ToLower(m.filter)
	m.filtered = nil
	for _, r := range m.runs {
		host := r.Hostname
		if host == "" {
			host = r.EnvoyID
		}
		if strings.Contains(strings.ToLower(host), f) ||
			strings.Contains(strings.ToLower(r.Status), f) {
			m.filtered = append(m.filtered, r)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m runsModel) Update(msg tea.Msg) (runsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case runsFetchedMsg:
		if msg.err == nil {
			m.runs = msg.runs
			m.applyFilter()
		}
		m.ready = true
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

// SelectedRun returns the currently selected run, or nil.
func (m runsModel) SelectedRun() *Run {
	if m.cursor >= 0 && m.cursor < len(m.filtered) {
		r := m.filtered[m.cursor]
		return &r
	}
	return nil
}

func (m runsModel) View() string {
	if !m.ready {
		return "\n  Loading runs..."
	}

	var b strings.Builder

	// Search bar.
	if m.editing {
		b.WriteString(fmt.Sprintf("  Search: %s_\n\n", m.filter))
	} else if m.filter != "" {
		b.WriteString(fmt.Sprintf("  Filter: %s (/ to edit, Esc to clear)\n\n", m.filter))
	}

	if len(m.filtered) == 0 {
		b.WriteString("  No runs found.\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("ID", 10)),
		TableHeaderStyle.Render(padRight("HOST", 20)),
		TableHeaderStyle.Render(padRight("STARTED", 18)),
		TableHeaderStyle.Render(padRight("STATUS", 12)),
		TableHeaderStyle.Render(padRight("CHANGED", 8)),
		TableHeaderStyle.Render(padRight("FAILED", 8)),
	))

	maxVisible := m.height - 8
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
		r := m.filtered[i]
		id := r.ID
		if len(id) > 8 {
			id = id[:8]
		}
		hostname := r.Hostname
		if hostname == "" {
			hostname = r.EnvoyID
			if len(hostname) > 18 {
				hostname = hostname[:18]
			}
		}
		line := fmt.Sprintf("  %s  %s  %s  %s  %s  %s",
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(id, 10)),
			padRight(hostname, 20),
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(formatTS(r.StartedAt), 18)),
			StyledStatus(r.Status),
			padRight(fmt.Sprintf("%d", r.ChangedCount), 8),
			padRight(fmt.Sprintf("%d", r.FailedCount), 8),
		)
		if i == m.cursor {
			line = TableSelectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(fmt.Sprintf("\n  %d runs", len(m.filtered)))
	if m.filter != "" {
		b.WriteString(fmt.Sprintf(" (filtered from %d)", len(m.runs)))
	}
	b.WriteString("\n")
	return b.String()
}
