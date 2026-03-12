// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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
// It also implements cmdrun.StdinRunner for testing stdin piping.
type mockCmdRunnerGeneric struct {
	calls      []string
	stdinCalls []mockStdinCall // records stdin data for RunWithStdin calls
	results    map[string]mockCmdResult
}

type mockStdinCall struct {
	command string
	stdin   []byte
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

func (m *mockCmdRunnerGeneric) RunWithStdin(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	m.calls = append(m.calls, call)
	m.stdinCalls = append(m.stdinCalls, mockStdinCall{command: call, stdin: stdin})
	if r, ok := m.results[call]; ok {
		return r.output, r.err
	}
	for pattern, r := range m.results {
		if strings.Contains(call, pattern) {
			return r.output, r.err
		}
	}
	return nil, nil
}
