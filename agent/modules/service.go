package modules

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ServiceExecutor manages service resources idempotently using systemctl.
// It checks is-active and is-enabled before taking action.
type ServiceExecutor struct {
	Runner CommandRunner
}

func (s *ServiceExecutor) runner() CommandRunner {
	if s.Runner != nil {
		return s.Runner
	}
	return &DefaultRunner{}
}

// serviceStatus returns active and enabled state in a single systemctl call,
// halving subprocess overhead compared to separate is-active + is-enabled.
func (s *ServiceExecutor) serviceStatus(ctx context.Context, name string) (active, enabled bool) {
	out, err := s.runner().Run(ctx, "systemctl", "show", "--no-pager",
		"--property=ActiveState,UnitFileState", name)
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			active = v == "active" || v == "activating"
		case "UnitFileState":
			enabled = v == "enabled" || v == "enabled-runtime"
		}
	}
	return active, enabled
}

// Execute applies the desired service state based on params.
// Supported params:
//   - service (or name): service name or comma-separated list (required)
//   - state: "running", "stopped", "restarted", or "reloaded"
//   - enabled: "true" or "false" (optional)
func (s *ServiceExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	nameParam := params["service"]
	if nameParam == "" {
		nameParam = params["name"]
	}
	result := ResourceResult{
		ResourceType: "service",
		ResourceName: nameParam,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if nameParam == "" {
		result.Error = fmt.Errorf("service executor: name param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "running"
	}
	enabledParam := params["enabled"]

	names := splitNames(nameParam)

	for _, name := range names {
		res := s.executeOne(ctx, name, state, enabledParam, dryRun)
		if res.Error != nil {
			result.Error = res.Error
			return result
		}
		if res.Changed {
			result.Changed = true
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

// executeOne applies the desired state for a single service.
func (s *ServiceExecutor) executeOne(ctx context.Context, name, state, enabledParam string, dryRun bool) ResourceResult {
	result := ResourceResult{
		ResourceType: "service",
		ResourceName: name,
	}

	active, enabled := s.serviceStatus(ctx, name)

	changed := false

	var stateCmd string
	switch state {
	case "running":
		if !active {
			stateCmd = "start"
			changed = true
		}
	case "stopped":
		if active {
			stateCmd = "stop"
			changed = true
		}
	case "restarted":
		stateCmd = "restart"
		changed = true
	case "reloaded":
		stateCmd = "reload"
		changed = true
	default:
		result.Error = fmt.Errorf("service executor: unknown state %q", state)
		return result
	}

	var enableCmd string
	if enabledParam == "true" && !enabled {
		enableCmd = "enable"
		changed = true
	} else if enabledParam == "false" && enabled {
		enableCmd = "disable"
		changed = true
	}

	if !changed {
		result.Action = ActionNone
		result.Changed = false
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	if stateCmd != "" {
		out, err := s.runner().Run(ctx, "systemctl", stateCmd, name)
		if err != nil {
			result.Error = fmt.Errorf("systemctl %s %s: %w: %s", stateCmd, name, err, string(out))
			return result
		}
	}

	if enableCmd != "" {
		out, err := s.runner().Run(ctx, "systemctl", enableCmd, name)
		if err != nil {
			result.Error = fmt.Errorf("systemctl %s %s: %w: %s", enableCmd, name, err, string(out))
			return result
		}
	}

	return result
}
