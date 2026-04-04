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

	dashboard dashboardModel
	envoys    envoysModel
}

// New creates the root TUI model.
func New(ds DataSource, refreshInterval time.Duration) Model {
	return Model{
		ds:              ds,
		refreshInterval: refreshInterval,
		activeView:      viewDashboard,
		dashboard:       newDashboardModel(ds),
		envoys:          newEnvoysModel(ds),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.dashboard.Init(),
		m.envoys.Init(),
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
		return m, nil

	case tea.KeyMsg:
		// Global keys (always handled unless help is showing).
		if m.showHelp {
			m.showHelp = false
			return m, nil
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
			next := (m.activeView + 1) % 5
			return m, m.switchView(next)
		case "shift+tab":
			next := (m.activeView + 4) % 5
			return m, m.switchView(next)
		case "esc":
			if len(m.navStack) > 0 {
				m.activeView = m.navStack[len(m.navStack)-1]
				m.navStack = m.navStack[:len(m.navStack)-1]
				return m, nil
			}
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
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) switchView(v viewID) tea.Cmd {
	m.navStack = nil
	m.activeView = v
	return m.refreshActive()
}

func (m Model) refreshActive() tea.Cmd {
	switch m.activeView {
	case viewDashboard:
		return m.dashboard.fetch()
	case viewEnvoys:
		return m.envoys.fetch()
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
	viewHeight := m.height - 4 // title + tab bar + status bar + padding
	switch m.activeView {
	case viewDashboard:
		content = m.dashboard.View()
	case viewEnvoys:
		content = m.envoys.View()
	case viewRuns:
		content = "\n  Runs view (Phase 2)"
	case viewRisk:
		content = "\n  Risk view (Phase 3)"
	case viewAudit:
		content = "\n  Audit view (Phase 3)"
	}

	// Constrain content height.
	contentLines := lipgloss.Height(content)
	if contentLines > viewHeight {
		// Simple truncation — proper scrolling comes in Phase 4.
		lines := splitLines(content)
		if viewHeight > 0 && viewHeight < len(lines) {
			content = joinLines(lines[:viewHeight])
		}
	}

	// Tab bar.
	tabBar := renderTabBar(m.activeView, m.width)

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
