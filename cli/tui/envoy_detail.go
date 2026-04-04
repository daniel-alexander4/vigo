// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type envoyDetailModel struct {
	envoy  Envoy
	runs   []Run
	ds     DataSource
	cursor int
	width  int
	height int
	ready  bool
}

type envoyDetailFetchedMsg struct {
	runs []Run
	err  error
}

func newEnvoyDetailModel(ds DataSource, envoy Envoy) envoyDetailModel {
	return envoyDetailModel{ds: ds, envoy: envoy}
}

func (m envoyDetailModel) fetch() tea.Cmd {
	envoyID := m.envoy.ID
	return func() tea.Msg {
		runs, err := m.ds.FetchRuns(20)
		if err != nil {
			return envoyDetailFetchedMsg{err: err}
		}
		// Filter runs for this envoy.
		var filtered []Run
		for _, r := range runs {
			if r.EnvoyID == envoyID {
				filtered = append(filtered, r)
			}
		}
		return envoyDetailFetchedMsg{runs: filtered}
	}
}

func (m envoyDetailModel) Update(msg tea.Msg) (envoyDetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case envoyDetailFetchedMsg:
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

// SelectedRun returns the currently selected run in the detail view, or nil.
func (m envoyDetailModel) SelectedRun() *Run {
	if m.cursor >= 0 && m.cursor < len(m.runs) {
		r := m.runs[m.cursor]
		return &r
	}
	return nil
}

func (m envoyDetailModel) View() string {
	e := m.envoy
	var b strings.Builder

	// Header.
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("Hostname:"), lipgloss.NewStyle().Bold(true).Foreground(ColorText).Render(e.Hostname)))
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("ID:"), e.ID))
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("IP:"), e.IPAddress))
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("Enrolled:"), formatTS(e.EnrolledAt)))
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("Last Seen:"), formatTS(e.LastSeen)))

	status := "active"
	if e.Revoked {
		status = "revoked"
	}
	b.WriteString(fmt.Sprintf("  %s %s\n\n", StatLabelStyle.Render("Status:"), StyledStatus(status)))

	if !m.ready {
		b.WriteString("  Loading runs...\n")
		return b.String()
	}

	// Recent runs.
	b.WriteString(SectionStyle.Render("Recent Runs"))
	b.WriteString("\n")

	if len(m.runs) == 0 {
		b.WriteString("  No runs found for this envoy.\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("ID", 10)),
		TableHeaderStyle.Render(padRight("STARTED", 18)),
		TableHeaderStyle.Render(padRight("STATUS", 12)),
		TableHeaderStyle.Render(padRight("CHANGED", 8)),
		TableHeaderStyle.Render(padRight("FAILED", 8)),
	))

	for i, r := range m.runs {
		id := r.ID
		if len(id) > 8 {
			id = id[:8]
		}
		line := fmt.Sprintf("  %s  %s  %s  %s  %s",
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(id, 10)),
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

	b.WriteString(fmt.Sprintf("\n  %d runs  (Enter to view detail, Esc to go back)\n", len(m.runs)))
	return b.String()
}
