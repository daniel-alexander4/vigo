// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type riskModel struct {
	risk   FleetRisk
	ds     DataSource
	cursor int
	width  int
	height int
	ready  bool
}

type riskFetchedMsg struct {
	risk FleetRisk
	err  error
}

func newRiskModel(ds DataSource) riskModel {
	return riskModel{ds: ds}
}

func (m riskModel) fetch() tea.Cmd {
	return func() tea.Msg {
		risk, err := m.ds.FetchRisk()
		return riskFetchedMsg{risk: risk, err: err}
	}
}

func (m riskModel) Update(msg tea.Msg) (riskModel, tea.Cmd) {
	switch msg := msg.(type) {
	case riskFetchedMsg:
		if msg.err == nil {
			m.risk = msg.risk
		}
		m.ready = true
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.risk.TopRisks)-1 {
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

func (m riskModel) View() string {
	if !m.ready {
		return "\n  Loading risk data..."
	}

	var b strings.Builder

	// Summary stats.
	b.WriteString(fmt.Sprintf("  %s %d / %d\n", StatLabelStyle.Render("Scored Envoys:"),
		m.risk.ScoredEnvoys, m.risk.TotalEnvoys))
	b.WriteString(fmt.Sprintf("  %s %.0f\n", StatLabelStyle.Render("Average Score:"), m.risk.AvgScore))
	b.WriteString(fmt.Sprintf("  %s %d\n\n", StatLabelStyle.Render("Highest Score:"), m.risk.MaxScore))

	// Distribution bars.
	b.WriteString(SectionStyle.Render("Distribution"))
	b.WriteString("\n")
	for _, level := range []string{"critical", "high", "medium", "low"} {
		count := m.risk.Distribution[level]
		bar := strings.Repeat("█", count)
		if count == 0 {
			bar = ""
		}
		label := lipgloss.NewStyle().Foreground(StatusColor(level)).Render(padRight(level, 10))
		barStyled := lipgloss.NewStyle().Foreground(StatusColor(level)).Render(bar)
		b.WriteString(fmt.Sprintf("  %s %s %d\n", label, barStyled, count))
	}
	b.WriteString("\n")

	// Top risks table.
	if len(m.risk.TopRisks) > 0 {
		b.WriteString(SectionStyle.Render("Highest Risk Envoys"))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  %s  %s  %s\n",
			TableHeaderStyle.Render(padRight("HOSTNAME", 24)),
			TableHeaderStyle.Render(padRight("SCORE", 8)),
			TableHeaderStyle.Render(padRight("LEVEL", 10)),
		))

		for i, r := range m.risk.TopRisks {
			line := fmt.Sprintf("  %s  %s  %s",
				padRight(r.Hostname, 24),
				padRight(fmt.Sprintf("%d", r.Score), 8),
				StyledStatus(r.Level),
			)
			if i == m.cursor {
				line = TableSelectedStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}

	return b.String()
}
