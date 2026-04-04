// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DashboardData is the composite data for the dashboard view.
type DashboardData struct {
	Envoys []Envoy
	Runs   []Run
	Risk   *FleetRisk
}

type dashboardModel struct {
	data   DashboardData
	ds     DataSource
	width  int
	height int
	scroll int
	ready  bool
}

type dashboardFetchedMsg struct {
	data DashboardData
	err  error
}

func newDashboardModel(ds DataSource) dashboardModel {
	return dashboardModel{ds: ds}
}

func (m dashboardModel) Init() tea.Cmd {
	return m.fetch()
}

func (m dashboardModel) fetch() tea.Cmd {
	return func() tea.Msg {
		var (
			wg      sync.WaitGroup
			envoys  []Envoy
			runs    []Run
			risk    *FleetRisk
			lastErr error
		)

		wg.Add(3)
		go func() {
			defer wg.Done()
			e, err := m.ds.FetchEnvoys()
			if err != nil {
				lastErr = err
				return
			}
			envoys = e
		}()
		go func() {
			defer wg.Done()
			r, err := m.ds.FetchRuns(10)
			if err != nil {
				lastErr = err
				return
			}
			runs = r
		}()
		go func() {
			defer wg.Done()
			r, err := m.ds.FetchRisk()
			if err != nil {
				// Risk is optional — don't fail.
				return
			}
			risk = &r
		}()
		wg.Wait()

		return dashboardFetchedMsg{
			data: DashboardData{Envoys: envoys, Runs: runs, Risk: risk},
			err:  lastErr,
		}
	}
}

func (m dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case dashboardFetchedMsg:
		if msg.err == nil {
			m.data = msg.data
		}
		m.ready = true
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			m.scroll++
		case "k", "up":
			if m.scroll > 0 {
				m.scroll--
			}
		}
	}
	return m, nil
}

func (m dashboardModel) View() string {
	if !m.ready {
		return "\n  Loading dashboard..."
	}

	var b strings.Builder

	// Stat cards.
	b.WriteString(m.renderStats())
	b.WriteString("\n")

	// Fleet table.
	b.WriteString(SectionStyle.Render("Fleet Status"))
	b.WriteString("\n")
	b.WriteString(m.renderFleetTable())
	b.WriteString("\n")

	// Recent runs.
	b.WriteString(SectionStyle.Render("Recent Runs"))
	b.WriteString("\n")
	b.WriteString(m.renderRunsTable())

	return b.String()
}

func (m dashboardModel) renderStats() string {
	total := len(m.data.Envoys)
	var online, converged, relapsed, failed int
	for _, e := range m.data.Envoys {
		if !e.Revoked {
			online++
		}
	}
	for _, r := range m.data.Runs {
		switch r.Status {
		case "converged":
			converged++
		case "relapsed":
			relapsed++
		case "failed":
			failed++
		}
	}

	fleet := renderStatCard("Fleet", m.width/4, []statLine{
		{label: "Total Envoys", value: fmt.Sprintf("%d", total)},
		{label: "Active", value: fmt.Sprintf("%d", online)},
	})

	convergence := renderStatCard("Convergence", m.width/4, []statLine{
		{label: "Converged", value: fmt.Sprintf("%d", converged), color: ColorGreen},
		{label: "Relapsed", value: fmt.Sprintf("%d", relapsed), color: ColorYellow},
		{label: "Failed", value: fmt.Sprintf("%d", failed), color: ColorRed},
	})

	riskCard := renderStatCard("Risk", m.width/4, m.riskStatLines())

	runsCard := renderStatCard("Runs", m.width/4, []statLine{
		{label: "Recent", value: fmt.Sprintf("%d", len(m.data.Runs))},
	})

	return lipgloss.JoinHorizontal(lipgloss.Top, fleet, convergence, riskCard, runsCard)
}

type statLine struct {
	label string
	value string
	color lipgloss.Color
}

func (m dashboardModel) riskStatLines() []statLine {
	if m.data.Risk == nil {
		return []statLine{{label: "Unavailable", value: "-"}}
	}
	r := m.data.Risk
	return []statLine{
		{label: "Avg Score", value: fmt.Sprintf("%.0f", r.AvgScore)},
		{label: "Max Score", value: fmt.Sprintf("%d", r.MaxScore)},
		{label: "Critical", value: fmt.Sprintf("%d", r.Distribution["critical"]), color: ColorRed},
		{label: "High", value: fmt.Sprintf("%d", r.Distribution["high"]), color: ColorPeach},
	}
}

func renderStatCard(title string, width int, lines []statLine) string {
	if width < 20 {
		width = 20
	}
	var content string
	content += StatHeaderStyle.Render(title) + "\n"
	for _, l := range lines {
		val := StatValueStyle.Render(l.value)
		if l.color != "" {
			val = lipgloss.NewStyle().Bold(true).Foreground(l.color).Render(l.value)
		}
		content += StatLabelStyle.Render(l.label+": ") + val + "\n"
	}
	return lipgloss.NewStyle().
		Width(width).
		Padding(0, 1).
		Render(content)
}

func (m dashboardModel) renderFleetTable() string {
	if len(m.data.Envoys) == 0 {
		return "  No envoys enrolled.\n"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("HOSTNAME", 24)),
		TableHeaderStyle.Render(padRight("IP", 18)),
		TableHeaderStyle.Render(padRight("LAST SEEN", 20)),
	))
	for _, e := range m.data.Envoys {
		b.WriteString(fmt.Sprintf("  %s  %s  %s\n",
			padRight(e.Hostname, 24),
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(e.IPAddress, 18)),
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(formatTS(e.LastSeen), 20)),
		))
	}
	return b.String()
}

func (m dashboardModel) renderRunsTable() string {
	if len(m.data.Runs) == 0 {
		return "  No recent runs.\n"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("ID", 10)),
		TableHeaderStyle.Render(padRight("HOST", 20)),
		TableHeaderStyle.Render(padRight("STATUS", 12)),
		TableHeaderStyle.Render(padRight("CHANGED", 8)),
		TableHeaderStyle.Render(padRight("FAILED", 8)),
	))
	for _, r := range m.data.Runs {
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
		b.WriteString(fmt.Sprintf("  %s  %s  %s  %s  %s\n",
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(id, 10)),
			padRight(hostname, 20),
			padRight(StyledStatus(r.Status), 12+lipgloss.Width(StyledStatus(r.Status))-len(r.Status)),
			padRight(fmt.Sprintf("%d", r.ChangedCount), 8),
			padRight(fmt.Sprintf("%d", r.FailedCount), 8),
		))
	}
	return b.String()
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func formatTS(ts string) string {
	if len(ts) >= 16 {
		return ts[:10] + " " + ts[11:16]
	}
	return ts
}
