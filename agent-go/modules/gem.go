// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// GemExecutor manages Ruby gems.
// Params:
//   - package (or name): gem name (required)
//   - version: specific version; empty means latest
//   - state: present (default), absent, or latest
//   - gem_cmd: gem command to use (default: "gem")
//   - no_document: "true" (default) to skip rdoc/ri generation
type GemExecutor struct {
	Runner CommandRunner
}

func (e *GemExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *GemExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
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
	gemCmd := params["gem_cmd"]
	if gemCmd == "" {
		gemCmd = "gem"
	}
	noDoc := params["no_document"] != "false" // default true

	result := ResourceResult{
		ResourceType: "gem",
		ResourceName: pkg,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if pkg == "" {
		result.Error = fmt.Errorf("gem executor: package or name param is required")
		return result
	}

	switch state {
	case "present":
		return e.ensurePresent(ctx, gemCmd, pkg, version, noDoc, dryRun, result)
	case "latest":
		return e.ensureLatest(ctx, gemCmd, pkg, noDoc, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, gemCmd, pkg, dryRun, result)
	default:
		result.Error = fmt.Errorf("gem executor: unknown state %q", state)
		return result
	}
}

func (e *GemExecutor) ensurePresent(ctx context.Context, gemCmd, pkg, version string, noDoc, dryRun bool, result ResourceResult) ResourceResult {
	installed := e.installedVersion(ctx, gemCmd, pkg)

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

	args := []string{"install", pkg}
	if version != "" {
		args = append(args, "--version", version)
	}
	if noDoc {
		args = append(args, "--no-document")
	}

	if out, err := e.runner().Run(ctx, gemCmd, args...); err != nil {
		result.Error = fmt.Errorf("%s install %s: %w: %s", gemCmd, pkg, err, string(out))
	}

	return result
}

func (e *GemExecutor) ensureLatest(ctx context.Context, gemCmd, pkg string, noDoc, dryRun bool, result ResourceResult) ResourceResult {
	installed := e.installedVersion(ctx, gemCmd, pkg)

	if installed == "" {
		result.Action = ActionCreated
		result.Changed = true
		if dryRun {
			return result
		}
		args := []string{"install", pkg}
		if noDoc {
			args = append(args, "--no-document")
		}
		if out, err := e.runner().Run(ctx, gemCmd, args...); err != nil {
			result.Error = fmt.Errorf("%s install %s: %w: %s", gemCmd, pkg, err, string(out))
		}
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true
	if dryRun {
		return result
	}

	args := []string{"update", pkg}
	if noDoc {
		args = append(args, "--no-document")
	}
	if out, err := e.runner().Run(ctx, gemCmd, args...); err != nil {
		result.Error = fmt.Errorf("%s update %s: %w: %s", gemCmd, pkg, err, string(out))
	}

	return result
}

func (e *GemExecutor) ensureAbsent(ctx context.Context, gemCmd, pkg string, dryRun bool, result ResourceResult) ResourceResult {
	if e.installedVersion(ctx, gemCmd, pkg) == "" {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, gemCmd, "uninstall", pkg, "--executables", "--all"); err != nil {
		result.Error = fmt.Errorf("%s uninstall %s: %w: %s", gemCmd, pkg, err, string(out))
	}

	return result
}

func (e *GemExecutor) installedVersion(ctx context.Context, gemCmd, pkg string) string {
	out, err := e.runner().Run(ctx, gemCmd, "list", "--exact", pkg)
	if err != nil {
		return ""
	}
	// Output format: "pkg (1.2.3)"
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, pkg+" (") {
			ver := strings.TrimPrefix(line, pkg+" (")
			ver = strings.TrimSuffix(ver, ")")
			// May contain multiple versions: "1.2.3, 1.2.2" — take first.
			if i := strings.Index(ver, ","); i >= 0 {
				ver = ver[:i]
			}
			return strings.TrimSpace(ver)
		}
	}
	return ""
}
