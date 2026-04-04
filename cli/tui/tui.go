// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type viewID int

const (
	viewDashboard viewID = iota
	viewEnvoys
	viewRuns
	viewRisk
	viewAudit
	// Detail views (pushed onto nav stack, not in tab bar).
	viewEnvoyDetail
	viewRunDetail
)

type tickMsg time.Time

// Model is the root bubbletea model.
type Model struct {
	ds              DataSource
	width, height   int
	activeView      viewID
	navStack        []viewID
	showHelp        bool
	refreshInterval time.Duration
	lastRefresh     time.Time
	lastErr         error

	dashboard   dashboardModel
	envoys      envoysModel
	envoyDetail envoyDetailModel
	runs        runsModel
	runDetail   runDetailModel
	risk        riskModel
	audit       auditModel
}

// New creates the root TUI model.
func New(ds DataSource, refreshInterval time.Duration) Model {
	return Model{
		ds:              ds,
		refreshInterval: refreshInterval,
		activeView:      viewDashboard,
		dashboard:       newDashboardModel(ds),
		envoys:          newEnvoysModel(ds),
		runs:            newRunsModel(ds),
		risk:            newRiskModel(ds),
		audit:           newAuditModel(ds),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.dashboard.Init(),
		m.tickCmd(),
	)
}

func (m Model) tickCmd() tea.Cmd {
	if m.refreshInterval <= 0 {
		return nil
	}
	return tea.Tick(m.refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.dashboard.width = msg.Width
		m.dashboard.height = msg.Height
		m.envoys.width = msg.Width
		m.envoys.height = msg.Height
		m.envoyDetail.width = msg.Width
		m.envoyDetail.height = msg.Height
		m.runs.width = msg.Width
		m.runs.height = msg.Height
		m.runDetail.width = msg.Width
		m.runDetail.height = msg.Height
		m.risk.width = msg.Width
		m.risk.height = msg.Height
		m.audit.width = msg.Width
		m.audit.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}

		// Don't intercept number keys when envoys filter is active.
		if m.activeView == viewEnvoys && m.envoys.editing {
			var cmd tea.Cmd
			m.envoys, cmd = m.envoys.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.showHelp = true
			return m, nil
		case "r":
			return m, m.refreshActive()
		case "1":
			return m, m.switchView(viewDashboard)
		case "2":
			return m, m.switchView(viewEnvoys)
		case "3":
			return m, m.switchView(viewRuns)
		case "4":
			return m, m.switchView(viewRisk)
		case "5":
			return m, m.switchView(viewAudit)
		case "tab":
			next := (m.topView() + 1) % 5
			return m, m.switchView(next)
		case "shift+tab":
			next := (m.topView() + 4) % 5
			return m, m.switchView(next)
		case "esc":
			if len(m.navStack) > 0 {
				m.activeView = m.navStack[len(m.navStack)-1]
				m.navStack = m.navStack[:len(m.navStack)-1]
				return m, nil
			}
		case "enter":
			return m, m.handleEnter()
		}

	case tickMsg:
		m.lastRefresh = time.Time(msg)
		cmds = append(cmds, m.refreshActive(), m.tickCmd())
		return m, tea.Batch(cmds...)

	case dashboardFetchedMsg:
		m.lastErr = msg.err
		if msg.err == nil {
			m.lastRefresh = time.Now()
		}
	case envoysFetchedMsg:
		m.lastErr = msg.err
		if msg.err == nil {
			m.lastRefresh = time.Now()
		}
	case runsFetchedMsg:
		m.lastErr = msg.err
		if msg.err == nil {
			m.lastRefresh = time.Now()
		}
	case riskFetchedMsg:
		m.lastErr = msg.err
		if msg.err == nil {
			m.lastRefresh = time.Now()
		}
	case auditFetchedMsg:
		m.lastErr = msg.err
		if msg.err == nil {
			m.lastRefresh = time.Now()
		}
	case envoyDetailFetchedMsg:
		m.lastErr = msg.err
	case runDetailFetchedMsg:
		m.lastErr = msg.err
	}

	// Delegate to active submodel.
	switch m.activeView {
	case viewDashboard:
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(msg)
		cmds = append(cmds, cmd)
	case viewEnvoys:
		var cmd tea.Cmd
		m.envoys, cmd = m.envoys.Update(msg)
		cmds = append(cmds, cmd)
	case viewEnvoyDetail:
		var cmd tea.Cmd
		m.envoyDetail, cmd = m.envoyDetail.Update(msg)
		cmds = append(cmds, cmd)
	case viewRuns:
		var cmd tea.Cmd
		m.runs, cmd = m.runs.Update(msg)
		cmds = append(cmds, cmd)
	case viewRunDetail:
		var cmd tea.Cmd
		m.runDetail, cmd = m.runDetail.Update(msg)
		cmds = append(cmds, cmd)
	case viewRisk:
		var cmd tea.Cmd
		m.risk, cmd = m.risk.Update(msg)
		cmds = append(cmds, cmd)
	case viewAudit:
		var cmd tea.Cmd
		m.audit, cmd = m.audit.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// topView returns the top-level view (for tab cycling), ignoring detail views.
func (m Model) topView() viewID {
	if m.activeView >= viewEnvoyDetail {
		if len(m.navStack) > 0 {
			return m.navStack[0]
		}
		return viewDashboard
	}
	return m.activeView
}

func (m *Model) switchView(v viewID) tea.Cmd {
	m.navStack = nil
	m.activeView = v
	return m.refreshActive()
}

func (m *Model) pushView(v viewID) {
	m.navStack = append(m.navStack, m.activeView)
	m.activeView = v
}

func (m *Model) handleEnter() tea.Cmd {
	switch m.activeView {
	case viewEnvoys:
		if e := m.envoys.SelectedEnvoy(); e != nil {
			m.envoyDetail = newEnvoyDetailModel(m.ds, *e)
			m.envoyDetail.width = m.width
			m.envoyDetail.height = m.height
			m.pushView(viewEnvoyDetail)
			return m.envoyDetail.fetch()
		}
	case viewEnvoyDetail:
		if r := m.envoyDetail.SelectedRun(); r != nil {
			m.runDetail = newRunDetailModel(m.ds, r.ID)
			m.runDetail.width = m.width
			m.runDetail.height = m.height
			m.pushView(viewRunDetail)
			return m.runDetail.fetch()
		}
	case viewRuns:
		if r := m.runs.SelectedRun(); r != nil {
			m.runDetail = newRunDetailModel(m.ds, r.ID)
			m.runDetail.width = m.width
			m.runDetail.height = m.height
			m.pushView(viewRunDetail)
			return m.runDetail.fetch()
		}
	}
	return nil
}

func (m Model) refreshActive() tea.Cmd {
	switch m.activeView {
	case viewDashboard:
		return m.dashboard.fetch()
	case viewEnvoys:
		return m.envoys.fetch()
	case viewRuns:
		return m.runs.fetch()
	case viewRisk:
		return m.risk.fetch()
	case viewAudit:
		return m.audit.fetch()
	case viewEnvoyDetail:
		return m.envoyDetail.fetch()
	case viewRunDetail:
		return m.runDetail.fetch()
	default:
		return nil
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return "Initializing..."
	}

	if m.showHelp {
		return renderHelp(m.width, m.height)
	}

	// Title.
	title := TitleStyle.Render("VIGO")

	// Active view content.
	var content string
	viewHeight := m.height - 4
	switch m.activeView {
	case viewDashboard:
		content = m.dashboard.View()
	case viewEnvoys:
		content = m.envoys.View()
	case viewEnvoyDetail:
		content = m.envoyDetail.View()
	case viewRuns:
		content = m.runs.View()
	case viewRunDetail:
		content = m.runDetail.View()
	case viewRisk:
		content = m.risk.View()
	case viewAudit:
		content = m.audit.View()
	}

	// Constrain content height.
	contentLines := lipgloss.Height(content)
	if contentLines > viewHeight {
		lines := splitLines(content)
		if viewHeight > 0 && viewHeight < len(lines) {
			content = joinLines(lines[:viewHeight])
		}
	}

	// Tab bar.
	tabBar := renderTabBar(m.topView(), m.width)

	// Status bar.
	statusBar := renderStatusBar(m.lastRefresh, m.refreshInterval, m.lastErr, m.width)

	return lipgloss.JoinVertical(lipgloss.Left,
		title,
		content,
		tabBar,
		statusBar,
	)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}
