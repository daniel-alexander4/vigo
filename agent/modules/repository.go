package modules

import (
	"context"
	"fmt"
	"time"

	"github.com/isogov/isogov/agent/modules/pkgmgr"
)

// RepositoryExecutor manages package repository resources idempotently.
// It adds or removes third-party repositories and their GPG signing keys
// using the distro's native tooling, then refreshes the package index.
//
// Supported params:
//   - name:     repository identifier, used for file naming (required)
//   - key_url:  URL to GPG signing key (optional)
//   - repo:     repository specification — APT deb line or DNF/YUM repo block (required for state=present)
//   - state:    "present" (default) or "absent"
type RepositoryExecutor struct {
	RepoMgr pkgmgr.RepoManager
}

func (r *RepositoryExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	result := ResourceResult{
		ResourceType: "repository",
		ResourceName: name,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if name == "" {
		result.Error = fmt.Errorf("repository executor: name param is required")
		return result
	}

	if r.RepoMgr == nil {
		result.Error = fmt.Errorf("repository executor: no repo manager available for this platform")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}

	switch state {
	case "present":
		return r.handlePresent(ctx, name, params, dryRun, result)
	case "absent":
		return r.handleAbsent(ctx, name, dryRun, result)
	default:
		result.Error = fmt.Errorf("repository executor: unknown state %q (expected present or absent)", state)
		return result
	}
}

func (r *RepositoryExecutor) handlePresent(ctx context.Context, name string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	repo := params["repo"]
	if repo == "" {
		result.Error = fmt.Errorf("repository executor: repo param is required for state=present")
		return result
	}
	keyURL := params["key_url"]

	present, err := r.RepoMgr.IsRepoPresent(ctx, name)
	if err != nil {
		result.Error = fmt.Errorf("check repo %s: %w", name, err)
		return result
	}

	if present {
		result.Action = ActionNone
		result.Changed = false
		return result
	}

	result.Action = ActionCreated
	result.Changed = true

	if dryRun {
		return result
	}

	if err := r.RepoMgr.AddRepo(ctx, name, keyURL, repo); err != nil {
		result.Error = fmt.Errorf("add repo %s: %w", name, err)
		return result
	}

	if err := r.RepoMgr.RefreshIndex(ctx); err != nil {
		result.Error = fmt.Errorf("refresh index after adding %s: %w", name, err)
		return result
	}

	return result
}

func (r *RepositoryExecutor) handleAbsent(ctx context.Context, name string, dryRun bool, result ResourceResult) ResourceResult {
	present, err := r.RepoMgr.IsRepoPresent(ctx, name)
	if err != nil {
		result.Error = fmt.Errorf("check repo %s: %w", name, err)
		return result
	}

	if !present {
		result.Action = ActionNone
		result.Changed = false
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if err := r.RepoMgr.RemoveRepo(ctx, name); err != nil {
		result.Error = fmt.Errorf("remove repo %s: %w", name, err)
		return result
	}

	if err := r.RepoMgr.RefreshIndex(ctx); err != nil {
		result.Error = fmt.Errorf("refresh index after removing %s: %w", name, err)
		return result
	}

	return result
}
