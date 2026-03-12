// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NpmExecutor manages Node.js packages via npm (global installs).
// Params:
//   - package (or name): package name (required)
//   - version: specific version; empty means latest
//   - state: present (default), absent, or latest
//   - npm_cmd: npm command to use (default: "npm")
type NpmExecutor struct {
	Runner CommandRunner
}

func (e *NpmExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *NpmExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
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
	npmCmd := params["npm_cmd"]
	if npmCmd == "" {
		npmCmd = "npm"
	}

	result := ResourceResult{
		ResourceType: "npm",
		ResourceName: pkg,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if pkg == "" {
		result.Error = fmt.Errorf("npm executor: package or name param is required")
		return result
	}

	switch state {
	case "present":
		return e.ensurePresent(ctx, npmCmd, pkg, version, dryRun, result)
	case "latest":
		return e.ensureLatest(ctx, npmCmd, pkg, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, npmCmd, pkg, dryRun, result)
	default:
		result.Error = fmt.Errorf("npm executor: unknown state %q", state)
		return result
	}
}

func (e *NpmExecutor) ensurePresent(ctx context.Context, npmCmd, pkg, version string, dryRun bool, result ResourceResult) ResourceResult {
	installed := e.installedVersion(ctx, npmCmd, pkg)

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
		spec = pkg + "@" + version
	}

	if out, err := e.runner().Run(ctx, npmCmd, "install", "-g", spec); err != nil {
		result.Error = fmt.Errorf("%s install -g %s: %w: %s", npmCmd, spec, err, string(out))
	}

	return result
}

func (e *NpmExecutor) ensureLatest(ctx context.Context, npmCmd, pkg string, dryRun bool, result ResourceResult) ResourceResult {
	installed := e.installedVersion(ctx, npmCmd, pkg)

	if installed == "" {
		result.Action = ActionCreated
	} else {
		result.Action = ActionUpdated
	}
	result.Changed = true

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, npmCmd, "install", "-g", pkg+"@latest"); err != nil {
		result.Error = fmt.Errorf("%s install -g %s@latest: %w: %s", npmCmd, pkg, err, string(out))
	}

	return result
}

func (e *NpmExecutor) ensureAbsent(ctx context.Context, npmCmd, pkg string, dryRun bool, result ResourceResult) ResourceResult {
	if e.installedVersion(ctx, npmCmd, pkg) == "" {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, npmCmd, "uninstall", "-g", pkg); err != nil {
		result.Error = fmt.Errorf("%s uninstall -g %s: %w: %s", npmCmd, pkg, err, string(out))
	}

	return result
}

func (e *NpmExecutor) installedVersion(ctx context.Context, npmCmd, pkg string) string {
	out, err := e.runner().Run(ctx, npmCmd, "list", "-g", pkg, "--depth=0", "--json")
	if err != nil {
		return ""
	}
	// Quick parse: look for "version": "x.y.z" after the package name.
	// npm list --json output: {"dependencies":{"pkg":{"version":"1.2.3"}}}
	s := string(out)
	// Find the package section.
	idx := strings.Index(s, `"`+pkg+`"`)
	if idx < 0 {
		return ""
	}
	rest := s[idx:]
	verIdx := strings.Index(rest, `"version"`)
	if verIdx < 0 {
		return ""
	}
	rest = rest[verIdx+len(`"version"`):]
	// Skip whitespace and colon.
	rest = strings.TrimLeft(rest, ": \t\n")
	rest = strings.TrimLeft(rest, `"`)
	if end := strings.Index(rest, `"`); end >= 0 {
		return rest[:end]
	}
	return ""
}
