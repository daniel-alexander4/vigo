// Package modules provides resource executor types for the Isogov agent.
package modules

import (
	"context"
	"os/exec"
)

// ActionTaken describes what action an executor performed on a resource.
type ActionTaken string

const (
	ActionNone    ActionTaken = "none"
	ActionCreated ActionTaken = "created"
	ActionUpdated ActionTaken = "updated"
	ActionDeleted ActionTaken = "deleted"
	ActionSkipped ActionTaken = "skipped"
)

// ResourceResult records the outcome of executing a single resource.
type ResourceResult struct {
	ModuleName   string
	ResourceType string
	ResourceName string
	Action       ActionTaken
	Changed      bool
	Error        error
	DurationMs   int32
}

// Executor is the interface that all resource type executors must implement.
type Executor interface {
	Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult
}

// CommandRunner abstracts command execution for testability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// DefaultRunner executes commands via os/exec.
type DefaultRunner struct{}

// Run executes a command and returns combined output.
func (d *DefaultRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
