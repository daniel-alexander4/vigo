// Package cmdrun defines a shared CommandRunner interface for testable
// command execution across agent packages.
package cmdrun

import (
	"context"
	"os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Default executes commands via os/exec.
type Default struct{}

// Run executes a command and returns combined output.
func (d *Default) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
