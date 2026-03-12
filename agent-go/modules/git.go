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
)

// GitExecutor clones or updates a git repository to a local path.
// Params:
//   - repo: repository URL (required)
//   - path (or name): local destination path (required)
//   - branch: branch/tag/ref to checkout (default: repo default)
//   - depth: shallow clone depth (e.g., "1" for shallow)
//   - state: present (default) or absent
//   - force: "true" to discard local changes on update (default: "false")
type GitExecutor struct {
	Runner CommandRunner
}

func (e *GitExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *GitExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	path := params["path"]
	if path == "" {
		path = params["name"]
	}
	repo := params["repo"]
	branch := params["branch"]
	depth := params["depth"]
	state := params["state"]
	if state == "" {
		state = "present"
	}
	force := params["force"] == "true"

	result := ResourceResult{
		ResourceType: "git",
		ResourceName: path,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if path == "" {
		result.Error = fmt.Errorf("git executor: path or name param is required")
		return result
	}

	switch state {
	case "present":
		if repo == "" {
			result.Error = fmt.Errorf("git executor: repo param is required for state=present")
			return result
		}
		return e.ensurePresent(ctx, repo, path, branch, depth, force, dryRun, result)
	case "absent":
		return e.ensureAbsent(path, dryRun, result)
	default:
		result.Error = fmt.Errorf("git executor: unknown state %q", state)
		return result
	}
}

func (e *GitExecutor) ensurePresent(ctx context.Context, repo, path, branch, depth string, force, dryRun bool, result ResourceResult) ResourceResult {
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		// Clone
		result.Action = ActionCreated
		result.Changed = true

		if dryRun {
			return result
		}

		args := []string{"clone"}
		if depth != "" {
			args = append(args, "--depth", depth)
		}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, repo, path)

		if out, err := e.runner().Run(ctx, "git", args...); err != nil {
			result.Error = fmt.Errorf("git clone: %w: %s", err, string(out))
		}
		return result
	}

	// Repo exists — check if update is needed.
	if branch != "" {
		currentBranch, err := e.currentRef(ctx, path)
		if err == nil && currentBranch != branch {
			result.Action = ActionUpdated
			result.Changed = true
			if dryRun {
				return result
			}

			if _, err := e.runner().Run(ctx, "git", "-C", path, "fetch", "--all"); err != nil {
				result.Error = fmt.Errorf("git fetch: %w", err)
				return result
			}

			checkoutArgs := []string{"-C", path, "checkout", branch}
			if force {
				checkoutArgs = []string{"-C", path, "checkout", "--force", branch}
			}
			if out, err := e.runner().Run(ctx, "git", checkoutArgs...); err != nil {
				result.Error = fmt.Errorf("git checkout %s: %w: %s", branch, err, string(out))
				return result
			}
			return result
		}
	}

	// Check if remote has new commits.
	if _, err := e.runner().Run(ctx, "git", "-C", path, "fetch"); err != nil {
		result.Error = fmt.Errorf("git fetch: %w", err)
		return result
	}

	localRev, err := e.runner().Run(ctx, "git", "-C", path, "rev-parse", "HEAD")
	if err != nil {
		result.Error = fmt.Errorf("git rev-parse HEAD: %w", err)
		return result
	}

	remoteRef := "@{u}"
	remoteRev, err := e.runner().Run(ctx, "git", "-C", path, "rev-parse", remoteRef)
	if err != nil {
		// No upstream tracking — already at desired state.
		result.Action = ActionNone
		return result
	}

	if strings.TrimSpace(string(localRev)) == strings.TrimSpace(string(remoteRev)) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	pullArgs := []string{"-C", path, "pull"}
	if force {
		pullArgs = []string{"-C", path, "reset", "--hard", remoteRef}
	}
	if out, err := e.runner().Run(ctx, "git", pullArgs...); err != nil {
		result.Error = fmt.Errorf("git pull: %w: %s", err, string(out))
	}

	return result
}

func (e *GitExecutor) ensureAbsent(path string, dryRun bool, result ResourceResult) ResourceResult {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if !dryRun {
		if err := os.RemoveAll(path); err != nil {
			result.Error = fmt.Errorf("removing %s: %w", path, err)
		}
	}

	return result
}

func (e *GitExecutor) currentRef(ctx context.Context, path string) (string, error) {
	out, err := e.runner().Run(ctx, "git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
