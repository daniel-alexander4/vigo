// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// ExecExecutor runs commands with idempotency guards. It supports:
//   - creates: skip if the specified file exists
//   - onlyif: skip if the command exits non-zero
//   - unless: skip if the command exits zero
//   - stdin: pipe value to command's stdin (avoids secrets in process args)
//   - secret_file: write value to 0600 temp file, replace {{secret_file}} in command, delete after
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
//   - stdin: value piped to command's stdin (keeps secrets out of process args)
//   - secret_file: value written to 0600 temp file; {{secret_file}} in command is replaced with the path
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

	if params["stdin"] != "" && params["secret_file"] != "" {
		result.Error = fmt.Errorf("exec executor: stdin and secret_file are mutually exclusive")
		return result
	}

	// Skip all guards when triggered by notify/subscribes.
	triggered := params["_triggered"] == "true"

	// Exec resources with subscribes but no guards only run when triggered.
	hasGuards := params["creates"] != "" || params["onlyif"] != "" || params["unless"] != ""
	if params["_subscribes"] == "true" && !triggered && !hasGuards {
		result.Action = ActionSkipped
		result.Changed = false
		return result
	}

	if !triggered {
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
	}

	// Command would run
	result.Action = ActionCreated
	result.Changed = true

	if dryRun {
		return result
	}

	// Execute the actual command, using stdin or secret_file if specified.
	var out []byte
	var err error

	switch {
	case params["stdin"] != "":
		out, err = e.runWithStdin(ctx, command, params["stdin"])
	case params["secret_file"] != "":
		out, err = e.runWithSecretFile(ctx, command, params["secret_file"])
	default:
		parts := strings.Fields(command)
		out, err = e.runner().Run(ctx, parts[0], parts[1:]...)
	}

	if err != nil {
		result.Error = fmt.Errorf("exec %s: %w: %s", command, err, string(out))
	}

	return result
}

// runWithStdin pipes the secret value to the command's stdin.
func (e *ExecExecutor) runWithStdin(ctx context.Context, command, stdin string) ([]byte, error) {
	parts := strings.Fields(command)
	if sr, ok := e.runner().(cmdrun.StdinRunner); ok {
		return sr.RunWithStdin(ctx, []byte(stdin), parts[0], parts[1:]...)
	}
	return nil, fmt.Errorf("exec executor: stdin param requires a runner that supports StdinRunner interface")
}

// runWithSecretFile writes the secret to a 0600 temp file, replaces
// {{secret_file}} in the command with the file path, runs the command,
// then removes the file.
func (e *ExecExecutor) runWithSecretFile(ctx context.Context, command, secret string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "hobnob-secret-*")
	if err != nil {
		return nil, fmt.Errorf("creating secret temp file: %w", err)
	}
	path := tmp.Name()
	defer os.Remove(path)

	if err := os.Chmod(path, 0o600); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("setting secret file permissions: %w", err)
	}
	if _, err := tmp.WriteString(secret); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("writing secret file: %w", err)
	}
	tmp.Close()

	// Use absolute path to prevent path confusion.
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	resolved := strings.ReplaceAll(command, "{{secret_file}}", absPath)
	parts := strings.Fields(resolved)
	return e.runner().Run(ctx, parts[0], parts[1:]...)
}
