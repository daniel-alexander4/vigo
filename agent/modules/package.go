package modules

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hobnob/hobnob/agent/modules/pkgmgr"
)

// PackageExecutor manages package resources idempotently. It queries the
// package manager for installed state before acting, and only installs,
// removes, or upgrades when necessary.
type PackageExecutor struct {
	PkgMgr pkgmgr.PackageManager
}

// Execute applies the desired package state based on params.
// Supported params:
//   - name: package name or comma-separated list (required)
//   - state: "present" (default) or "absent"
//   - version: desired version (optional, triggers upgrade if different;
//     applies to all packages when using a comma-separated list)
func (p *PackageExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	nameParam := params["name"]
	result := ResourceResult{
		ResourceType: "package",
		ResourceName: nameParam,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if nameParam == "" {
		result.Error = fmt.Errorf("package executor: name param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}
	version := params["version"]

	// Split comma-separated package names.
	names := splitNames(nameParam)

	for _, name := range names {
		res := p.executeOne(ctx, name, state, version, dryRun)
		if res.Error != nil {
			result.Error = res.Error
			return result
		}
		if res.Changed {
			result.Changed = true
			// Use the most significant action (created/deleted > updated > none).
			if result.Action == "" || result.Action == ActionNone {
				result.Action = res.Action
			}
		}
	}

	if result.Action == "" {
		result.Action = ActionNone
	}
	return result
}

// splitNames splits a comma-separated name string into trimmed, non-empty names.
func splitNames(s string) []string {
	parts := strings.Split(s, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			names = append(names, p)
		}
	}
	return names
}

// executeOne applies the desired state for a single package.
func (p *PackageExecutor) executeOne(ctx context.Context, name, state, version string, dryRun bool) ResourceResult {
	result := ResourceResult{
		ResourceType: "package",
		ResourceName: name,
	}

	installed, currentVersion, err := p.PkgMgr.IsInstalled(ctx, name)
	if err != nil {
		result.Error = fmt.Errorf("query package %s: %w", name, err)
		return result
	}

	switch state {
	case "present":
		if installed {
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
			result.Action = ActionNone
			result.Changed = false
			return result
		}
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
