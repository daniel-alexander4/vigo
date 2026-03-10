// Package modules provides resource executor types for the Hobnob agent.
package modules

import (
	"context"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// ActionTaken describes what action an executor performed on a resource.
type ActionTaken string

const (
	ActionNone    ActionTaken = "already in desired state"
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

// CommandRunner is an alias for the shared command runner interface.
type CommandRunner = cmdrun.Runner

// DefaultRunner is an alias for the shared default command runner.
type DefaultRunner = cmdrun.Default
