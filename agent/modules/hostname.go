package modules

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// HostnameExecutor manages the system hostname.
// Params:
//   - hostname (or name): desired hostname
type HostnameExecutor struct {
	HostnameFile string // override for testing (default /etc/hostname)
	Runner       CommandRunner
}

func (e *HostnameExecutor) hostnameFile() string {
	if e.HostnameFile != "" {
		return e.HostnameFile
	}
	return "/etc/hostname"
}

func (e *HostnameExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *HostnameExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	desired := params["hostname"]
	if desired == "" {
		desired = params["name"]
	}

	result := ResourceResult{
		ResourceType: "hostname",
		ResourceName: desired,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if desired == "" {
		result.Error = fmt.Errorf("hostname executor: hostname or name param is required")
		return result
	}

	current := e.getCurrentHostname()

	if current == desired {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	// Try hostnamectl first (systemd), fall back to writing /etc/hostname.
	if _, err := e.runner().Run(ctx, "hostnamectl", "set-hostname", desired); err != nil {
		// Fallback: write /etc/hostname directly.
		if err := os.WriteFile(e.hostnameFile(), []byte(desired+"\n"), 0644); err != nil {
			result.Error = fmt.Errorf("writing %s: %w", e.hostnameFile(), err)
			return result
		}
		// Also set the runtime hostname.
		if _, err := e.runner().Run(ctx, "hostname", desired); err != nil {
			result.Error = fmt.Errorf("setting runtime hostname: %w", err)
		}
	}

	return result
}

func (e *HostnameExecutor) getCurrentHostname() string {
	data, err := os.ReadFile(e.hostnameFile())
	if err != nil {
		// Fallback to os.Hostname().
		h, _ := os.Hostname()
		return h
	}
	return strings.TrimSpace(string(data))
}
