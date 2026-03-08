package modules

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// ExecExecutor runs commands with idempotency guards. It supports:
//   - creates: skip if the specified file exists
//   - onlyif: skip if the command exits non-zero
//   - unless: skip if the command exits zero
type ExecExecutor struct {
	Runner CommandRunner
}

func (e *ExecExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

// Execute runs a command if idempotency guards allow it.
// Supported params:
//   - command: the command to run (required)
//   - creates: file path -- skip if it exists
//   - onlyif: guard command -- skip if it exits non-zero
//   - unless: guard command -- skip if it exits zero
//   - cwd: working directory (optional, not enforced through Runner interface)
//   - user: run as user (optional, not enforced through Runner interface)
func (e *ExecExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	command := params["command"]
	result := ResourceResult{
		ResourceType: "exec",
		ResourceName: command,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if command == "" {
		result.Error = fmt.Errorf("exec executor: command param is required")
		return result
	}

	// Guard 1: creates -- skip if file exists
	if creates := params["creates"]; creates != "" {
		if _, err := os.Stat(creates); err == nil {
			result.Action = ActionSkipped
			result.Changed = false
			return result
		}
	}

	// Guards 2 and 3 require running commands. We skip them during dry-run
	// to avoid side effects from guard commands.
	if !dryRun {
		// Guard 2: onlyif -- skip if command fails (non-zero exit)
		if onlyif := params["onlyif"]; onlyif != "" {
			parts := strings.Fields(onlyif)
			_, err := e.runner().Run(ctx, parts[0], parts[1:]...)
			if err != nil {
				result.Action = ActionSkipped
				result.Changed = false
				return result
			}
		}

		// Guard 3: unless -- skip if command succeeds (zero exit)
		if unless := params["unless"]; unless != "" {
			parts := strings.Fields(unless)
			_, err := e.runner().Run(ctx, parts[0], parts[1:]...)
			if err == nil {
				result.Action = ActionSkipped
				result.Changed = false
				return result
			}
		}
	}

	// Command would run
	result.Action = ActionCreated
	result.Changed = true

	if dryRun {
		return result
	}

	// Execute the actual command
	parts := strings.Fields(command)
	out, err := e.runner().Run(ctx, parts[0], parts[1:]...)
	if err != nil {
		result.Error = fmt.Errorf("exec %s: %w: %s", command, err, string(out))
	}

	return result
}
