// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PipExecutor manages Python packages via pip.
// Params:
//   - package (or name): package name (required)
//   - version: specific version (e.g., "1.2.3"); empty means latest
//   - state: present (default), absent, or latest
//   - pip_cmd: pip command to use (default: "pip3")
//   - virtualenv: path to virtualenv (prepends <venv>/bin/ to pip_cmd)
//   - extra_args: additional pip arguments
type PipExecutor struct {
	Runner CommandRunner
}

func (e *PipExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *PipExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	pkg := params["package"]
	if pkg == "" {
		pkg = params["name"]
	}
	version := params["version"]
	state := params["state"]
	if state == "" {
		state = "present"
	}
	pipCmd := params["pip_cmd"]
	if pipCmd == "" {
		pipCmd = "pip3"
	}
	if venv := params["virtualenv"]; venv != "" {
		pipCmd = venv + "/bin/pip"
	}
	extraArgs := params["extra_args"]

	result := ResourceResult{
		ResourceType: "pip",
		ResourceName: pkg,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if pkg == "" {
		result.Error = fmt.Errorf("pip executor: package or name param is required")
		return result
	}

	switch state {
	case "present":
		return e.ensurePresent(ctx, pipCmd, pkg, version, extraArgs, dryRun, result)
	case "latest":
		return e.ensureLatest(ctx, pipCmd, pkg, extraArgs, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, pipCmd, pkg, extraArgs, dryRun, result)
	default:
		result.Error = fmt.Errorf("pip executor: unknown state %q", state)
		return result
	}
}

func (e *PipExecutor) ensurePresent(ctx context.Context, pipCmd, pkg, version, extraArgs string, dryRun bool, result ResourceResult) ResourceResult {
	installed := e.installedVersion(ctx, pipCmd, pkg)

	if installed != "" {
		if version == "" || installed == version {
			result.Action = ActionNone
			return result
		}
	}

	result.Changed = true
	if installed == "" {
		result.Action = ActionCreated
	} else {
		result.Action = ActionUpdated
	}

	if dryRun {
		return result
	}

	spec := pkg
	if version != "" {
		spec = pkg + "==" + version
	}

	args := []string{"install", spec}
	if extraArgs != "" {
		args = append(args, strings.Fields(extraArgs)...)
	}

	if out, err := e.runner().Run(ctx, pipCmd, args...); err != nil {
		result.Error = fmt.Errorf("%s install %s: %w: %s", pipCmd, spec, err, string(out))
	}

	return result
}

func (e *PipExecutor) ensureLatest(ctx context.Context, pipCmd, pkg, extraArgs string, dryRun bool, result ResourceResult) ResourceResult {
	installed := e.installedVersion(ctx, pipCmd, pkg)

	if installed == "" {
		result.Action = ActionCreated
		result.Changed = true
		if dryRun {
			return result
		}

		args := []string{"install", pkg}
		if extraArgs != "" {
			args = append(args, strings.Fields(extraArgs)...)
		}
		if out, err := e.runner().Run(ctx, pipCmd, args...); err != nil {
			result.Error = fmt.Errorf("%s install %s: %w: %s", pipCmd, pkg, err, string(out))
		}
		return result
	}

	// Check if upgrade is available.
	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	args := []string{"install", "--upgrade", pkg}
	if extraArgs != "" {
		args = append(args, strings.Fields(extraArgs)...)
	}
	if out, err := e.runner().Run(ctx, pipCmd, args...); err != nil {
		result.Error = fmt.Errorf("%s install --upgrade %s: %w: %s", pipCmd, pkg, err, string(out))
	}

	return result
}

func (e *PipExecutor) ensureAbsent(ctx context.Context, pipCmd, pkg, extraArgs string, dryRun bool, result ResourceResult) ResourceResult {
	if e.installedVersion(ctx, pipCmd, pkg) == "" {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	args := []string{"uninstall", "-y", pkg}
	if extraArgs != "" {
		args = append(args, strings.Fields(extraArgs)...)
	}
	if out, err := e.runner().Run(ctx, pipCmd, args...); err != nil {
		result.Error = fmt.Errorf("%s uninstall %s: %w: %s", pipCmd, pkg, err, string(out))
	}

	return result
}

// installedVersion returns the installed version of a pip package, or "".
func (e *PipExecutor) installedVersion(ctx context.Context, pipCmd, pkg string) string {
	out, err := e.runner().Run(ctx, pipCmd, "show", pkg)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Version: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Version: "))
		}
	}
	return ""
}
