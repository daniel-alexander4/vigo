// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type runDetailModel struct {
	detail RunDetail
	runID  string
	ds     DataSource
	cursor int
	width  int
	height int
	ready  bool
}

type runDetailFetchedMsg struct {
	detail RunDetail
	err    error
}

func newRunDetailModel(ds DataSource, runID string) runDetailModel {
	return runDetailModel{ds: ds, runID: runID}
}

func (m runDetailModel) fetch() tea.Cmd {
	id := m.runID
	return func() tea.Msg {
		detail, err := m.ds.FetchRunDetail(id)
		return runDetailFetchedMsg{detail: detail, err: err}
	}
}

func (m runDetailModel) Update(msg tea.Msg) (runDetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case runDetailFetchedMsg:
		if msg.err == nil {
			m.detail = msg.detail
		}
		m.ready = true
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.detail.Results)-1 {
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

func (m runDetailModel) View() string {
	if !m.ready {
		return "\n  Loading run detail..."
	}

	r := m.detail.Run
	var b strings.Builder

	// Header.
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("Run:"), r.ID))
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("Envoy:"), r.EnvoyID))
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("Started:"), formatTS(r.StartedAt)))
	b.WriteString(fmt.Sprintf("  %s %s\n", StatLabelStyle.Render("Status:"), StyledStatus(r.Status)))
	b.WriteString(fmt.Sprintf("  %s %d changed, %d failed\n\n",
		StatLabelStyle.Render("Results:"), r.ChangedCount, r.FailedCount))

	if len(m.detail.Results) == 0 {
		b.WriteString("  No resource results.\n")
		return b.String()
	}

	// Results table.
	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("MODULE", 18)),
		TableHeaderStyle.Render(padRight("TYPE", 12)),
		TableHeaderStyle.Render(padRight("RESOURCE", 24)),
		TableHeaderStyle.Render(padRight("ACTION", 10)),
		TableHeaderStyle.Render(padRight("ERROR", 30)),
	))

	maxVisible := m.height - 12
	if maxVisible < 3 {
		maxVisible = 3
	}
	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.detail.Results) {
		end = len(m.detail.Results)
	}

	for i := start; i < end; i++ {
		res := m.detail.Results[i]
		errStr := "-"
		errStyle := lipgloss.NewStyle().Foreground(ColorSubtext1)
		if res.ErrorMessage != "" {
			errStr = res.ErrorMessage
			if len(errStr) > 28 {
				errStr = errStr[:28] + ".."
			}
			errStyle = lipgloss.NewStyle().Foreground(ColorRed)
		}

		line := fmt.Sprintf("  %s  %s  %s  %s  %s",
			padRight(res.ModuleName, 18),
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(res.ResourceType, 12)),
			padRight(res.ResourceName, 24),
			StyledStatus(res.ActionTaken),
			errStyle.Render(padRight(errStr, 30)),
		)
		if i == m.cursor {
			line = TableSelectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(fmt.Sprintf("\n  %d resources\n", len(m.detail.Results)))
	return b.String()
}
