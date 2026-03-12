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

// KernelModuleExecutor manages kernel modules via modprobe.
// Params:
//   - module (or name): kernel module name
//   - state: present (default, loaded + persisted), absent (unloaded + depersisted),
//            loaded (runtime only), unloaded (runtime only)
//   - persist: "true" (default for present/absent) to write /etc/modules-load.d/
//   - params: optional module parameters
type KernelModuleExecutor struct {
	ModulesDir string // override for testing (default /etc/modules-load.d)
	Runner     CommandRunner
}

func (e *KernelModuleExecutor) modulesDir() string {
	if e.ModulesDir != "" {
		return e.ModulesDir
	}
	return "/etc/modules-load.d"
}

func (e *KernelModuleExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *KernelModuleExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	modName := params["module"]
	if modName == "" {
		modName = params["name"]
	}
	state := params["state"]
	if state == "" {
		state = "present"
	}
	modParams := params["params"]

	result := ResourceResult{
		ResourceType: "kernel_module",
		ResourceName: modName,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if modName == "" {
		result.Error = fmt.Errorf("kernel_module executor: module or name param is required")
		return result
	}

	switch state {
	case "present":
		return e.ensurePresent(ctx, modName, modParams, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, modName, dryRun, result)
	case "loaded":
		return e.ensureLoaded(ctx, modName, modParams, dryRun, result)
	case "unloaded":
		return e.ensureUnloaded(ctx, modName, dryRun, result)
	default:
		result.Error = fmt.Errorf("kernel_module executor: unknown state %q", state)
		return result
	}
}

func (e *KernelModuleExecutor) ensurePresent(ctx context.Context, modName, modParams string, dryRun bool, result ResourceResult) ResourceResult {
	loaded := e.isLoaded(ctx, modName)
	persisted := e.isPersisted(modName)

	if loaded && persisted {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	result.Action = ActionCreated

	if dryRun {
		return result
	}

	if !loaded {
		args := []string{modName}
		if modParams != "" {
			args = append(args, strings.Fields(modParams)...)
		}
		if _, err := e.runner().Run(ctx, "modprobe", args...); err != nil {
			result.Error = fmt.Errorf("modprobe %s: %w", modName, err)
			return result
		}
	}

	if !persisted {
		if err := e.persist(modName); err != nil {
			result.Error = fmt.Errorf("persisting %s: %w", modName, err)
		}
	}

	return result
}

func (e *KernelModuleExecutor) ensureAbsent(ctx context.Context, modName string, dryRun bool, result ResourceResult) ResourceResult {
	loaded := e.isLoaded(ctx, modName)
	persisted := e.isPersisted(modName)

	if !loaded && !persisted {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if loaded {
		if _, err := e.runner().Run(ctx, "modprobe", "-r", modName); err != nil {
			result.Error = fmt.Errorf("modprobe -r %s: %w", modName, err)
			return result
		}
	}

	if persisted {
		if err := e.depersist(modName); err != nil {
			result.Error = fmt.Errorf("depersisting %s: %w", modName, err)
		}
	}

	return result
}

func (e *KernelModuleExecutor) ensureLoaded(ctx context.Context, modName, modParams string, dryRun bool, result ResourceResult) ResourceResult {
	if e.isLoaded(ctx, modName) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionCreated
	result.Changed = true
	if !dryRun {
		args := []string{modName}
		if modParams != "" {
			args = append(args, strings.Fields(modParams)...)
		}
		if _, err := e.runner().Run(ctx, "modprobe", args...); err != nil {
			result.Error = fmt.Errorf("modprobe %s: %w", modName, err)
		}
	}
	return result
}

func (e *KernelModuleExecutor) ensureUnloaded(ctx context.Context, modName string, dryRun bool, result ResourceResult) ResourceResult {
	if !e.isLoaded(ctx, modName) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true
	if !dryRun {
		if _, err := e.runner().Run(ctx, "modprobe", "-r", modName); err != nil {
			result.Error = fmt.Errorf("modprobe -r %s: %w", modName, err)
		}
	}
	return result
}

func (e *KernelModuleExecutor) isLoaded(ctx context.Context, modName string) bool {
	out, err := e.runner().Run(ctx, "lsmod")
	if err != nil {
		return false
	}
	// lsmod output format: "Module Size Used by\n<name> <size> <used> ..."
	// Module names with dashes become underscores in lsmod.
	normalized := strings.ReplaceAll(modName, "-", "_")
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 1 && fields[0] == normalized {
			return true
		}
	}
	return false
}

func (e *KernelModuleExecutor) isPersisted(modName string) bool {
	confPath := e.confPath(modName)
	_, err := os.Stat(confPath)
	return err == nil
}

func (e *KernelModuleExecutor) persist(modName string) error {
	if err := os.MkdirAll(e.modulesDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(e.confPath(modName), []byte(modName+"\n"), 0644)
}

func (e *KernelModuleExecutor) depersist(modName string) error {
	return os.Remove(e.confPath(modName))
}

func (e *KernelModuleExecutor) confPath(modName string) string {
	return filepath.Join(e.modulesDir(), "hobnob-"+modName+".conf")
}
