package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SysctlExecutor manages kernel parameters via sysctl.
// Params:
//   - key (or name): sysctl key (e.g., net.core.somaxconn)
//   - value: desired value (required for state=present)
//   - state: present (default) or absent
//   - persist: "true" (default) to write /etc/sysctl.d/<key>.conf
type SysctlExecutor struct {
	SysctlDir string // override for testing (default /etc/sysctl.d)
	Runner    CommandRunner
}

func (e *SysctlExecutor) sysctlDir() string {
	if e.SysctlDir != "" {
		return e.SysctlDir
	}
	return "/etc/sysctl.d"
}

func (e *SysctlExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *SysctlExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	key := params["key"]
	if key == "" {
		key = params["name"]
	}
	value := params["value"]
	state := params["state"]
	if state == "" {
		state = "present"
	}
	persist := params["persist"] != "false" // default true

	result := ResourceResult{
		ResourceType: "sysctl",
		ResourceName: key,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if key == "" {
		result.Error = fmt.Errorf("sysctl executor: key or name param is required")
		return result
	}

	switch state {
	case "present":
		if value == "" {
			result.Error = fmt.Errorf("sysctl executor: value param is required for state=present")
			return result
		}
		return e.ensurePresent(ctx, key, value, persist, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, key, persist, dryRun, result)
	default:
		result.Error = fmt.Errorf("sysctl executor: unknown state %q", state)
		return result
	}
}

func (e *SysctlExecutor) ensurePresent(ctx context.Context, key, value string, persist, dryRun bool, result ResourceResult) ResourceResult {
	// Check current runtime value.
	current, err := e.getCurrentValue(ctx, key)
	if err != nil {
		// Key might not exist yet — that's OK, we'll set it.
		current = ""
	}

	// Check persisted value.
	persistedValue := e.getPersistedValue(key)
	confPath := e.confPath(key)
	desired := key + " = " + value

	runtimeMatch := strings.TrimSpace(current) == strings.TrimSpace(value)
	persistMatch := !persist || persistedValue == desired

	if runtimeMatch && persistMatch {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if current == "" {
		result.Action = ActionCreated
	} else {
		result.Action = ActionUpdated
	}

	if dryRun {
		return result
	}

	// Apply runtime value.
	if !runtimeMatch {
		if _, err := e.runner().Run(ctx, "sysctl", "-w", key+"="+value); err != nil {
			result.Error = fmt.Errorf("sysctl -w %s=%s: %w", key, value, err)
			return result
		}
	}

	// Persist to /etc/sysctl.d/.
	if persist && !persistMatch {
		if err := os.MkdirAll(e.sysctlDir(), 0755); err != nil {
			result.Error = fmt.Errorf("creating %s: %w", e.sysctlDir(), err)
			return result
		}
		if err := os.WriteFile(confPath, []byte(desired+"\n"), 0644); err != nil {
			result.Error = fmt.Errorf("writing %s: %w", confPath, err)
			return result
		}
	}

	return result
}

func (e *SysctlExecutor) ensureAbsent(ctx context.Context, key string, persist, dryRun bool, result ResourceResult) ResourceResult {
	confPath := e.confPath(key)
	_, persistErr := os.Stat(confPath)
	persistExists := persistErr == nil

	if !persistExists {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true
	if !dryRun {
		if err := os.Remove(confPath); err != nil && !os.IsNotExist(err) {
			result.Error = fmt.Errorf("removing %s: %w", confPath, err)
			return result
		}
		// Reload sysctl to drop the value.
		e.runner().Run(ctx, "sysctl", "--system")
	}
	return result
}

func (e *SysctlExecutor) getCurrentValue(ctx context.Context, key string) (string, error) {
	out, err := e.runner().Run(ctx, "sysctl", "-n", key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (e *SysctlExecutor) getPersistedValue(key string) string {
	data, err := os.ReadFile(e.confPath(key))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// confPath returns the sysctl.d config file path for a key.
// Dots in the key are replaced with dashes for the filename.
func (e *SysctlExecutor) confPath(key string) string {
	safeName := strings.ReplaceAll(key, ".", "-")
	return filepath.Join(e.sysctlDir(), "99-hobnob-"+safeName+".conf")
}
