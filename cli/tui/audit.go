// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type auditModel struct {
	events []AuditEvent
	ds     DataSource
	cursor int
	width  int
	height int
	ready  bool
}

type auditFetchedMsg struct {
	events []AuditEvent
	err    error
}

func newAuditModel(ds DataSource) auditModel {
	return auditModel{ds: ds}
}

func (m auditModel) fetch() tea.Cmd {
	return func() tea.Msg {
		events, err := m.ds.FetchAudit(100)
		return auditFetchedMsg{events: events, err: err}
	}
}

func (m auditModel) Update(msg tea.Msg) (auditModel, tea.Cmd) {
	switch msg := msg.(type) {
	case auditFetchedMsg:
		if msg.err == nil {
			m.events = msg.events
		}
		m.ready = true
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.events)-1 {
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

func (m auditModel) View() string {
	if !m.ready {
		return "\n  Loading audit log..."
	}
	if len(m.events) == 0 {
		return "\n  No audit events."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("TIMESTAMP", 20)),
		TableHeaderStyle.Render(padRight("TYPE", 22)),
		TableHeaderStyle.Render(padRight("OPERATOR", 14)),
		TableHeaderStyle.Render(padRight("SUMMARY", 40)),
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
	if end > len(m.events) {
		end = len(m.events)
	}

	for i := start; i < end; i++ {
		e := m.events[i]
		ts := e.Timestamp.Format("2006-01-02 15:04:05")
		summary := e.Summary
		if len(summary) > 38 {
			summary = summary[:38] + ".."
		}
		operator := e.Operator
		if operator == "" {
			operator = "-"
		}
		line := fmt.Sprintf("  %s  %s  %s  %s",
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(ts, 20)),
			padRight(e.EventType, 22),
			lipgloss.NewStyle().Foreground(ColorSubtext1).Render(padRight(operator, 14)),
			padRight(summary, 40),
		)
		if i == m.cursor {
			line = TableSelectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(fmt.Sprintf("\n  %d events\n", len(m.events)))
	return b.String()
}
