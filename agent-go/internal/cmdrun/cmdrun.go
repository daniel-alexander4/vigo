// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package cmdrun defines a shared CommandRunner interface for testable
// command execution across agent packages.
package cmdrun

import (
	"bytes"
	"context"
	"os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// StdinRunner extends Runner with stdin pipe support. Executors that need
// to pass secrets without exposing them in process arguments should check
// for this interface.
type StdinRunner interface {
	Runner
	RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
}

// Default executes commands via os/exec.
type Default struct{}

// Run executes a command and returns combined output.
func (d *Default) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// RunWithStdin executes a command with data piped to stdin.
func (d *Default) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.CombinedOutput()
}
