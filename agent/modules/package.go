package modules

import (
	"context"
	"fmt"
	"time"

	"github.com/isogov/isogov/agent/modules/pkgmgr"
)

// PackageExecutor manages package resources idempotently. It queries the
// package manager for installed state before acting, and only installs,
// removes, or upgrades when necessary.
type PackageExecutor struct {
	PkgMgr pkgmgr.PackageManager
}

// Execute applies the desired package state based on params.
// Supported params:
//   - name: package name (required)
//   - state: "present" (default) or "absent"
//   - version: desired version (optional, triggers upgrade if different)
func (p *PackageExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	result := ResourceResult{
		ResourceType: "package",
		ResourceName: name,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if name == "" {
		result.Error = fmt.Errorf("package executor: name param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}
	version := params["version"]

	installed, currentVersion, err := p.PkgMgr.IsInstalled(ctx, name)
	if err != nil {
		result.Error = fmt.Errorf("query package %s: %w", name, err)
		return result
	}

	switch state {
	case "present":
		if installed {
			// If a specific version is requested and it differs, upgrade
			if version != "" && currentVersion != version {
				result.Action = ActionUpdated
				result.Changed = true
				if dryRun {
					return result
				}
				if err := p.PkgMgr.Upgrade(ctx, name, version); err != nil {
					result.Error = fmt.Errorf("upgrade package %s to %s: %w", name, version, err)
				}
				return result
			}
			// Already installed at desired version (or no version specified)
			result.Action = ActionNone
			result.Changed = false
			return result
		}
		// Not installed -- install
		result.Action = ActionCreated
		result.Changed = true
		if dryRun {
			return result
		}
		if err := p.PkgMgr.Install(ctx, name, version); err != nil {
			result.Error = fmt.Errorf("install package %s: %w", name, err)
		}
		return result

	case "absent":
		if !installed {
			result.Action = ActionNone
			result.Changed = false
			return result
		}
		result.Action = ActionDeleted
		result.Changed = true
		if dryRun {
			return result
		}
		if err := p.PkgMgr.Remove(ctx, name); err != nil {
			result.Error = fmt.Errorf("remove package %s: %w", name, err)
		}
		return result

	default:
		result.Error = fmt.Errorf("package executor: unknown state %q (expected present or absent)", state)
		return result
	}
}
