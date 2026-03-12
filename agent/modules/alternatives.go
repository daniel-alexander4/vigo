// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// AlternativesExecutor manages system alternatives via update-alternatives.
// Params:
//   - name: alternative name (e.g., "java", "editor", "python")
//   - path: path to the alternative binary (e.g., /usr/bin/java-17)
//   - link: symlink path (e.g., /usr/bin/java) — usually auto-detected
//   - priority: numeric priority (default: "50")
//   - state: present (default, install + set), absent (remove alternative)
type AlternativesExecutor struct {
	Runner CommandRunner
}

func (e *AlternativesExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *AlternativesExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	path := params["path"]
	state := params["state"]
	if state == "" {
		state = "present"
	}

	result := ResourceResult{
		ResourceType: "alternatives",
		ResourceName: name,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if name == "" {
		result.Error = fmt.Errorf("alternatives executor: name param is required")
		return result
	}

	switch state {
	case "present":
		if path == "" {
			result.Error = fmt.Errorf("alternatives executor: path param is required for state=present")
			return result
		}
		return e.ensurePresent(ctx, name, path, params, dryRun, result)
	case "absent":
		if path == "" {
			result.Error = fmt.Errorf("alternatives executor: path param is required for state=absent")
			return result
		}
		return e.ensureAbsent(ctx, name, path, dryRun, result)
	default:
		result.Error = fmt.Errorf("alternatives executor: unknown state %q", state)
		return result
	}
}

func (e *AlternativesExecutor) ensurePresent(ctx context.Context, name, path string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	link := params["link"]
	if link == "" {
		link = "/usr/bin/" + name
	}
	priority := params["priority"]
	if priority == "" {
		priority = "50"
	}

	// Check if the path is already installed and active.
	current := e.currentChoice(ctx, name)
	installed := e.isInstalled(ctx, name, path)

	if installed && current == path {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if !installed {
		result.Action = ActionCreated
	} else {
		result.Action = ActionUpdated
	}

	if dryRun {
		return result
	}

	// Install the alternative if not present.
	if !installed {
		if out, err := e.runner().Run(ctx, "update-alternatives", "--install", link, name, path, priority); err != nil {
			result.Error = fmt.Errorf("update-alternatives --install: %w: %s", err, string(out))
			return result
		}
	}

	// Set it as the active choice.
	if current != path {
		if out, err := e.runner().Run(ctx, "update-alternatives", "--set", name, path); err != nil {
			result.Error = fmt.Errorf("update-alternatives --set: %w: %s", err, string(out))
			return result
		}
	}

	return result
}

func (e *AlternativesExecutor) ensureAbsent(ctx context.Context, name, path string, dryRun bool, result ResourceResult) ResourceResult {
	if !e.isInstalled(ctx, name, path) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, "update-alternatives", "--remove", name, path); err != nil {
		result.Error = fmt.Errorf("update-alternatives --remove: %w: %s", err, string(out))
	}

	return result
}

// currentChoice returns the currently selected alternative path for a name.
func (e *AlternativesExecutor) currentChoice(ctx context.Context, name string) string {
	// Try reading the symlink directly (works on Debian and RHEL).
	target, err := os.Readlink("/etc/alternatives/" + name)
	if err == nil {
		return target
	}

	// Fall back to query.
	out, err := e.runner().Run(ctx, "update-alternatives", "--query", name)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Value: ") {
			return strings.TrimPrefix(line, "Value: ")
		}
	}
	return ""
}

// isInstalled checks if a specific path is registered as an alternative.
func (e *AlternativesExecutor) isInstalled(ctx context.Context, name, path string) bool {
	out, err := e.runner().Run(ctx, "update-alternatives", "--query", name)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "Alternative: "+path {
			return true
		}
	}
	return false
}
