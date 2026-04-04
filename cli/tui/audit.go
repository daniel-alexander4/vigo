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
	events   []AuditEvent
	filtered []AuditEvent
	ds       DataSource
	cursor   int
	filter   string
	editing  bool
	width    int
	height   int
	ready    bool
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

func (m *auditModel) applyFilter() {
	if m.filter == "" {
		m.filtered = m.events
		return
	}
	f := strings.ToLower(m.filter)
	m.filtered = nil
	for _, e := range m.events {
		if strings.Contains(strings.ToLower(e.EventType), f) ||
			strings.Contains(strings.ToLower(e.Summary), f) ||
			strings.Contains(strings.ToLower(e.Operator), f) {
			m.filtered = append(m.filtered, e)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m auditModel) Update(msg tea.Msg) (auditModel, tea.Cmd) {
	switch msg := msg.(type) {
	case auditFetchedMsg:
		if msg.err == nil {
			m.events = msg.events
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

func (m auditModel) View() string {
	if !m.ready {
		return "\n  Loading audit log..."
	}

	var b strings.Builder

	if m.editing {
		b.WriteString(fmt.Sprintf("  Search: %s_\n\n", m.filter))
	} else if m.filter != "" {
		b.WriteString(fmt.Sprintf("  Filter: %s (/ to edit, Esc to clear)\n\n", m.filter))
	}

	if len(m.filtered) == 0 {
		b.WriteString("  No audit events.\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("  %s  %s  %s  %s\n",
		TableHeaderStyle.Render(padRight("TIMESTAMP", 20)),
		TableHeaderStyle.Render(padRight("TYPE", 22)),
		TableHeaderStyle.Render(padRight("OPERATOR", 14)),
		TableHeaderStyle.Render(padRight("SUMMARY", 40)),
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
		e := m.filtered[i]
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

	b.WriteString(fmt.Sprintf("\n  %d events", len(m.filtered)))
	if m.filter != "" {
		b.WriteString(fmt.Sprintf(" (filtered from %d)", len(m.events)))
	}
	b.WriteString("\n")
	return b.String()
}
