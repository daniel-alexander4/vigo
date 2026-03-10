package modules

import (
	"context"
	"fmt"
	"time"
)

// ProcessExecutor checks and manages OS processes idempotently using pgrep/kill.
// It can verify a process is running or stop it with a configurable signal.
type ProcessExecutor struct {
	Runner CommandRunner
}

func (p *ProcessExecutor) runner() CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return &DefaultRunner{}
}

// isRunning checks if a process is currently running.
// When match is "pattern", uses pgrep -f (pattern match); otherwise pgrep -x (exact).
func (p *ProcessExecutor) isRunning(ctx context.Context, name, match string) bool {
	flag := "-x"
	if match == "pattern" {
		flag = "-f"
	}
	_, err := p.runner().Run(ctx, "pgrep", flag, name)
	return err == nil
}

// Execute applies the desired process state based on params.
// Supported params:
//   - process (or name): process name or pattern (required)
//   - state: "running" (default) or "stopped"
//   - signal: signal name to send when stopping (default "TERM")
//   - match: "exact" (default, pgrep -x) or "pattern" (pgrep -f)
func (p *ProcessExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	nameParam := params["process"]
	if nameParam == "" {
		nameParam = params["name"]
	}
	result := ResourceResult{
		ResourceType: "process",
		ResourceName: nameParam,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if nameParam == "" {
		result.Error = fmt.Errorf("process executor: name param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "running"
	}

	signal := params["signal"]
	if signal == "" {
		signal = "TERM"
	}

	match := params["match"]
	if match == "" {
		match = "exact"
	}

	running := p.isRunning(ctx, nameParam, match)

	switch state {
	case "running":
		if running {
			result.Action = ActionNone
			result.Changed = false
		} else {
			result.Action = ActionNone
			result.Changed = false
			result.Error = fmt.Errorf("process %q is not running", nameParam)
		}
	case "stopped":
		if !running {
			result.Action = ActionNone
			result.Changed = false
		} else {
			result.Action = ActionDeleted
			result.Changed = true
			if !dryRun {
				killFlag := "-x"
				if match == "pattern" {
					killFlag = "-f"
				}
				out, err := p.runner().Run(ctx, "pkill", fmt.Sprintf("-%s", signal), killFlag, nameParam)
				if err != nil {
					result.Error = fmt.Errorf("pkill -%s %s %s: %w: %s", signal, killFlag, nameParam, err, string(out))
					return result
				}
			}
		}
	default:
		result.Error = fmt.Errorf("process executor: unknown state %q", state)
	}

	return result
}
