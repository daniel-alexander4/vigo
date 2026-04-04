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
	runs   []Run
	ds     DataSource
	cursor int
	width  int
	height int
	ready  bool
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

func (m runsModel) Update(msg tea.Msg) (runsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case runsFetchedMsg:
		if msg.err == nil {
			m.runs = msg.runs
		}
		m.ready = true
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.runs)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		}
	}
	return m, nil
}

// SelectedRun returns the currently selected run, or nil.
func (m runsModel) SelectedRun() *Run {
	if m.cursor >= 0 && m.cursor < len(m.runs) {
		r := m.runs[m.cursor]
		return &r
	}
	return nil
}

func (m runsModel) View() string {
	if !m.ready {
		return "\n  Loading runs..."
	}
	if len(m.runs) == 0 {
		return "\n  No runs found."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("ID", 10)),
		TableHeaderStyle.Render(padRight("HOST", 20)),
		TableHeaderStyle.Render(padRight("STARTED", 18)),
		TableHeaderStyle.Render(padRight("STATUS", 12)),
		TableHeaderStyle.Render(padRight("CHANGED", 8)),
		TableHeaderStyle.Render(padRight("FAILED", 8)),
	))

	maxVisible := m.height - 6
	if maxVisible < 3 {
		maxVisible = 3
	}
	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.runs) {
		end = len(m.runs)
	}

	for i := start; i < end; i++ {
		r := m.runs[i]
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

	b.WriteString(fmt.Sprintf("\n  %d runs\n", len(m.runs)))
	return b.String()
}
