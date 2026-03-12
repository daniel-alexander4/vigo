// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"time"
)

// RebootExecutor triggers a controlled system reboot. Designed to be used with
// subscribes/notify so it only fires when a dependency actually changes (e.g.,
// kernel upgrade, kernel_module change). Without subscribes, it reboots on
// every convergence — the validator warns about this.
//
// Params:
//   - delay: minutes to wait before rebooting (default: "0" = immediate)
//   - message: wall message to broadcast before reboot
//   - reboot_required_file: only reboot if this file exists (e.g., /var/run/reboot-required)
//   - state: present (default, triggers reboot) or absent (cancels pending reboot)
type RebootExecutor struct {
	Runner CommandRunner
}

func (e *RebootExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *RebootExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	if name == "" {
		name = "reboot"
	}
	state := params["state"]
	if state == "" {
		state = "present"
	}

	result := ResourceResult{
		ResourceType: "reboot",
		ResourceName: name,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	switch state {
	case "present":
		return e.triggerReboot(ctx, params, dryRun, result)
	case "absent":
		return e.cancelReboot(ctx, dryRun, result)
	default:
		result.Error = fmt.Errorf("reboot executor: unknown state %q", state)
		return result
	}
}

func (e *RebootExecutor) triggerReboot(ctx context.Context, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	// If reboot_required_file is set, only proceed if the file exists.
	reqFile := params["reboot_required_file"]
	if reqFile != "" {
		if _, err := os.Stat(reqFile); os.IsNotExist(err) {
			result.Action = ActionNone
			return result
		}
	}

	delay := params["delay"]
	if delay == "" {
		delay = "0"
	}
	message := params["message"]
	if message == "" {
		message = "System reboot triggered by Hobnob"
	}

	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	// Use shutdown -r for controlled reboot with delay support.
	args := []string{"-r", "+" + delay, message}
	if out, err := e.runner().Run(ctx, "shutdown", args...); err != nil {
		result.Error = fmt.Errorf("shutdown -r +%s: %w: %s", delay, err, string(out))
	}

	return result
}

func (e *RebootExecutor) cancelReboot(ctx context.Context, dryRun bool, result ResourceResult) ResourceResult {
	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, "shutdown", "-c"); err != nil {
		result.Error = fmt.Errorf("shutdown -c: %w: %s", err, string(out))
	}

	return result
}
