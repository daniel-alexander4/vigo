package modules

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ProcessExecutor checks and manages OS processes idempotently using pgrep/kill.
// It can verify a process is running or stop it with a configurable signal.
//
// Supported params:
//   - process (or name): process name or pattern (required)
//   - state: "running" (default) or "stopped"
//   - signal: signal name to send when stopping (default "TERM")
//   - match: "exact" (default, pgrep -x) or "pattern" (pgrep -f)
//   - count: exact expected process count (mutually exclusive with min/max)
//   - min: minimum acceptable process count
//   - max: maximum acceptable process count
//   - restart: "systemctl" to auto-restart when not running (state=running only)
//   - restart_unit: systemd unit name if different from process name
//   - kill_over: "true" to kill excess processes when count > max
type ProcessExecutor struct {
	Runner CommandRunner
}

func (p *ProcessExecutor) runner() CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return &DefaultRunner{}
}

// processCount returns how many matching processes are running.
// When match is "pattern", uses pgrep -f; otherwise pgrep -x.
func (p *ProcessExecutor) processCount(ctx context.Context, name, match string) int {
	flag := "-x"
	if match == "pattern" {
		flag = "-f"
	}
	out, err := p.runner().Run(ctx, "pgrep", flag, name)
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

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

	// Parse count constraints.
	countStr := params["count"]
	minStr := params["min"]
	maxStr := params["max"]

	if countStr != "" && (minStr != "" || maxStr != "") {
		result.Error = fmt.Errorf("process executor: count is mutually exclusive with min/max")
		return result
	}

	var hasCount, hasMin, hasMax bool
	var exactCount, minCount, maxCount int
	var err error

	if countStr != "" {
		exactCount, err = strconv.Atoi(countStr)
		if err != nil {
			result.Error = fmt.Errorf("process executor: invalid count %q: %w", countStr, err)
			return result
		}
		hasCount = true
	}
	if minStr != "" {
		minCount, err = strconv.Atoi(minStr)
		if err != nil {
			result.Error = fmt.Errorf("process executor: invalid min %q: %w", minStr, err)
			return result
		}
		hasMin = true
	}
	if maxStr != "" {
		maxCount, err = strconv.Atoi(maxStr)
		if err != nil {
			result.Error = fmt.Errorf("process executor: invalid max %q: %w", maxStr, err)
			return result
		}
		hasMax = true
	}

	if hasMin && hasMax && minCount > maxCount {
		result.Error = fmt.Errorf("process executor: min (%d) > max (%d)", minCount, maxCount)
		return result
	}

	restartMethod := params["restart"]
	restartUnit := params["restart_unit"]
	killOver := params["kill_over"] == "true"

	count := p.processCount(ctx, nameParam, match)

	switch state {
	case "running":
		return p.handleRunning(ctx, params, nameParam, match, signal, count, hasCount, exactCount, hasMin, minCount, hasMax, maxCount, restartMethod, restartUnit, killOver, dryRun)
	case "stopped":
		return p.handleStopped(ctx, nameParam, match, signal, count, dryRun, result)
	default:
		result.Error = fmt.Errorf("process executor: unknown state %q", state)
		return result
	}
}

func (p *ProcessExecutor) handleRunning(ctx context.Context, _ map[string]string, name, match, signal string, count int, hasCount bool, exactCount int, hasMin bool, minCount int, hasMax bool, maxCount int, restartMethod, restartUnit string, killOver bool, dryRun bool) ResourceResult {
	result := ResourceResult{
		ResourceType: "process",
		ResourceName: name,
	}

	hasConstraints := hasCount || hasMin || hasMax

	// No count constraints — simple running check.
	if !hasConstraints {
		if count > 0 {
			result.Action = ActionNone
			return result
		}
		// Not running.
		if restartMethod != "" {
			return p.restartProcess(ctx, name, restartMethod, restartUnit, dryRun)
		}
		result.Error = fmt.Errorf("process %q is not running", name)
		return result
	}

	// Count constraints active.
	tooFew := false
	tooMany := false

	if hasCount {
		tooFew = count < exactCount
		tooMany = count > exactCount
	} else {
		if hasMin {
			tooFew = count < minCount
		}
		if hasMax {
			tooMany = count > maxCount
		}
	}

	if !tooFew && !tooMany {
		result.Action = ActionNone
		return result
	}

	// Too few processes.
	if tooFew {
		if restartMethod != "" {
			return p.restartProcess(ctx, name, restartMethod, restartUnit, dryRun)
		}
		if hasCount {
			result.Error = fmt.Errorf("process %q: expected %d, found %d", name, exactCount, count)
		} else {
			result.Error = fmt.Errorf("process %q: expected min %d, found %d", name, minCount, count)
		}
		return result
	}

	// Too many processes.
	if tooMany && killOver {
		target := maxCount
		if hasCount {
			target = exactCount
		}
		excess := count - target
		return p.killExcess(ctx, name, match, signal, excess, dryRun)
	}

	// Too many but no kill_over — report error.
	if hasCount {
		result.Error = fmt.Errorf("process %q: expected %d, found %d", name, exactCount, count)
	} else {
		result.Error = fmt.Errorf("process %q: expected max %d, found %d", name, maxCount, count)
	}
	return result
}

func (p *ProcessExecutor) handleStopped(ctx context.Context, name, match, signal string, count int, dryRun bool, result ResourceResult) ResourceResult {
	if count == 0 {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	killFlag := "-x"
	if match == "pattern" {
		killFlag = "-f"
	}
	out, err := p.runner().Run(ctx, "pkill", fmt.Sprintf("-%s", signal), killFlag, name)
	if err != nil {
		result.Error = fmt.Errorf("pkill -%s %s %s: %w: %s", signal, killFlag, name, err, string(out))
	}
	return result
}

// restartProcess restarts a process via the configured restart method.
func (p *ProcessExecutor) restartProcess(ctx context.Context, name, method, unit string, dryRun bool) ResourceResult {
	result := ResourceResult{
		ResourceType: "process",
		ResourceName: name,
		Action:       ActionUpdated,
		Changed:      true,
	}

	if method != "systemctl" {
		result.Error = fmt.Errorf("process executor: unknown restart method %q (supported: systemctl)", method)
		return result
	}

	if dryRun {
		return result
	}

	svcName := unit
	if svcName == "" {
		svcName = name
	}
	out, err := p.runner().Run(ctx, "systemctl", "restart", svcName)
	if err != nil {
		result.Error = fmt.Errorf("systemctl restart %s: %w: %s", svcName, err, string(out))
	}
	return result
}

// killExcess kills a specific number of excess processes using kill on individual PIDs.
func (p *ProcessExecutor) killExcess(ctx context.Context, name, match, signal string, excess int, dryRun bool) ResourceResult {
	result := ResourceResult{
		ResourceType: "process",
		ResourceName: name,
		Action:       ActionDeleted,
		Changed:      true,
	}

	if dryRun {
		return result
	}

	// Get individual PIDs.
	flag := "-x"
	if match == "pattern" {
		flag = "-f"
	}
	out, err := p.runner().Run(ctx, "pgrep", flag, name)
	if err != nil {
		result.Error = fmt.Errorf("pgrep %s %s: %w", flag, name, err)
		return result
	}

	pids := strings.Split(strings.TrimSpace(string(out)), "\n")
	killed := 0
	for _, pid := range pids {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		if killed >= excess {
			break
		}
		out, err := p.runner().Run(ctx, "kill", fmt.Sprintf("-%s", signal), pid)
		if err != nil {
			result.Error = fmt.Errorf("kill -%s %s: %w: %s", signal, pid, err, string(out))
			return result
		}
		killed++
	}

	return result
}
