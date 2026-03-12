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

// TimezoneExecutor manages the system timezone.
// Params:
//   - timezone (or name): e.g., "America/New_York", "UTC"
type TimezoneExecutor struct {
	Runner CommandRunner
}

func (e *TimezoneExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *TimezoneExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	desired := params["timezone"]
	if desired == "" {
		desired = params["name"]
	}

	result := ResourceResult{
		ResourceType: "timezone",
		ResourceName: desired,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if desired == "" {
		result.Error = fmt.Errorf("timezone executor: timezone or name param is required")
		return result
	}

	current := getCurrentTimezone()

	if current == desired {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	// Try timedatectl first (systemd).
	if _, err := e.runner().Run(ctx, "timedatectl", "set-timezone", desired); err != nil {
		// Fallback: symlink /etc/localtime.
		zonePath := filepath.Join("/usr/share/zoneinfo", desired)
		if _, statErr := os.Stat(zonePath); statErr != nil {
			result.Error = fmt.Errorf("unknown timezone %q: %s not found", desired, zonePath)
			return result
		}
		os.Remove("/etc/localtime")
		if err := os.Symlink(zonePath, "/etc/localtime"); err != nil {
			result.Error = fmt.Errorf("symlinking /etc/localtime: %w", err)
			return result
		}
		// Write /etc/timezone for Debian-based systems.
		os.WriteFile("/etc/timezone", []byte(desired+"\n"), 0644)
	}

	return result
}

// getCurrentTimezone reads the current timezone from /etc/timezone or
// resolves the /etc/localtime symlink.
func getCurrentTimezone() string {
	// Try /etc/timezone first (Debian/Ubuntu).
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(data)); tz != "" {
			return tz
		}
	}

	// Resolve /etc/localtime symlink.
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	// Target is like /usr/share/zoneinfo/America/New_York.
	const prefix = "/usr/share/zoneinfo/"
	if idx := strings.Index(target, prefix); idx >= 0 {
		return target[idx+len(prefix):]
	}
	return ""
}
