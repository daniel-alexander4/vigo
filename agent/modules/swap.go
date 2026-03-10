package modules

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// SwapExecutor manages swap files and their fstab entries.
// Params:
//   - path (or name): path to swap file (required)
//   - size: size in MB (required for state=present; creates file with fallocate)
//   - state: present (default, ensure swap file + fstab + swapon),
//            absent (swapoff + remove fstab + remove file)
//   - priority: swap priority for fstab (optional, e.g. "-1")
type SwapExecutor struct {
	FstabFile string // override for testing (default /etc/fstab)
	ProcSwaps string // override for testing (default /proc/swaps)
	Runner    CommandRunner
}

func (e *SwapExecutor) fstabFile() string {
	if e.FstabFile != "" {
		return e.FstabFile
	}
	return "/etc/fstab"
}

func (e *SwapExecutor) procSwaps() string {
	if e.ProcSwaps != "" {
		return e.ProcSwaps
	}
	return "/proc/swaps"
}

func (e *SwapExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *SwapExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	path := params["path"]
	if path == "" {
		path = params["name"]
	}
	state := params["state"]
	if state == "" {
		state = "present"
	}
	size := params["size"]
	priority := params["priority"]

	result := ResourceResult{
		ResourceType: "swap",
		ResourceName: path,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if path == "" {
		result.Error = fmt.Errorf("swap executor: path or name param is required")
		return result
	}

	switch state {
	case "present":
		if size == "" {
			result.Error = fmt.Errorf("swap executor: size param is required for state=present")
			return result
		}
		return e.ensurePresent(ctx, path, size, priority, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, path, dryRun, result)
	default:
		result.Error = fmt.Errorf("swap executor: unknown state %q", state)
		return result
	}
}

func (e *SwapExecutor) ensurePresent(ctx context.Context, path, size, priority string, dryRun bool, result ResourceResult) ResourceResult {
	fileExists := e.swapFileExists(path)
	active := e.isSwapActive(path)
	inFstab := e.isInFstab(path)

	if fileExists && active && inFstab {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if fileExists {
		result.Action = ActionUpdated
	} else {
		result.Action = ActionCreated
	}

	if dryRun {
		return result
	}

	// Create swap file if it doesn't exist.
	if !fileExists {
		out, err := e.runner().Run(ctx, "fallocate", "-l", size+"M", path)
		if err != nil {
			result.Error = fmt.Errorf("fallocate -l %sM %s: %w: %s", size, path, err, string(out))
			return result
		}
		out, err = e.runner().Run(ctx, "chmod", "0600", path)
		if err != nil {
			result.Error = fmt.Errorf("chmod 0600 %s: %w: %s", path, err, string(out))
			return result
		}
		out, err = e.runner().Run(ctx, "mkswap", path)
		if err != nil {
			result.Error = fmt.Errorf("mkswap %s: %w: %s", path, err, string(out))
			return result
		}
	}

	// Activate swap if not active.
	if !active {
		args := []string{path}
		if priority != "" {
			args = []string{"-p", priority, path}
		}
		out, err := e.runner().Run(ctx, "swapon", args...)
		if err != nil {
			result.Error = fmt.Errorf("swapon %s: %w: %s", path, err, string(out))
			return result
		}
	}

	// Add to fstab if not present.
	if !inFstab {
		opts := "sw"
		if priority != "" {
			opts = "sw,pri=" + priority
		}
		line := fmt.Sprintf("%s\tnone\tswap\t%s\t0\t0", path, opts)
		f, err := os.OpenFile(e.fstabFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			result.Error = fmt.Errorf("opening fstab: %w", err)
			return result
		}
		defer f.Close()
		if _, err := fmt.Fprintln(f, line); err != nil {
			result.Error = fmt.Errorf("writing fstab: %w", err)
			return result
		}
	}

	return result
}

func (e *SwapExecutor) ensureAbsent(ctx context.Context, path string, dryRun bool, result ResourceResult) ResourceResult {
	fileExists := e.swapFileExists(path)
	active := e.isSwapActive(path)
	inFstab := e.isInFstab(path)

	if !fileExists && !active && !inFstab {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	// Deactivate swap.
	if active {
		out, err := e.runner().Run(ctx, "swapoff", path)
		if err != nil {
			result.Error = fmt.Errorf("swapoff %s: %w: %s", path, err, string(out))
			return result
		}
	}

	// Remove from fstab.
	if inFstab {
		if err := e.removeFromFstab(path); err != nil {
			result.Error = err
			return result
		}
	}

	// Remove file.
	if fileExists {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			result.Error = fmt.Errorf("removing %s: %w", path, err)
			return result
		}
	}

	return result
}

func (e *SwapExecutor) swapFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (e *SwapExecutor) isSwapActive(path string) bool {
	f, err := os.Open(e.procSwaps())
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 1 && fields[0] == path {
			return true
		}
	}
	return false
}

func (e *SwapExecutor) isInFstab(path string) bool {
	f, err := os.Open(e.fstabFile())
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == path && fields[2] == "swap" {
			return true
		}
	}
	return false
}

func (e *SwapExecutor) removeFromFstab(path string) error {
	f, err := os.Open(e.fstabFile())
	if err != nil {
		return fmt.Errorf("reading fstab: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 && fields[0] == path && fields[2] == "swap" {
				continue
			}
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading fstab: %w", err)
	}

	return writeLines(e.fstabFile(), lines)
}
