package modules

import (
	"context"
	"strings"
)

// mockCmdResult represents a canned command result.
type mockCmdResult struct {
	output []byte
	err    error
}

// mockCmdRunnerGeneric is a reusable mock CommandRunner for tests.
type mockCmdRunnerGeneric struct {
	calls   []string
	results map[string]mockCmdResult
}

func newMockCmdRunnerGeneric() *mockCmdRunnerGeneric {
	return &mockCmdRunnerGeneric{results: make(map[string]mockCmdResult)}
}

func (m *mockCmdRunnerGeneric) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	m.calls = append(m.calls, call)
	// Try exact match first
	if r, ok := m.results[call]; ok {
		return r.output, r.err
	}
	// Then partial match
	for pattern, r := range m.results {
		if strings.Contains(call, pattern) {
			return r.output, r.err
		}
	}
	return nil, nil
}
