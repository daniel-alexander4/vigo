// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Compile-time interface check
var _ Executor = (*ServiceExecutor)(nil)

// showOutput builds a mock systemctl show response.
func showOutput(active, enabled bool) []byte {
	as := "inactive"
	if active {
		as = "active"
	}
	us := "disabled"
	if enabled {
		us = "enabled"
	}
	return []byte("ActiveState=" + as + "\nUnitFileState=" + us + "\n")
}

// showKey returns the mock key for systemctl show of a service.
func showKey(name string) string {
	return "systemctl show --no-pager --property=ActiveState,UnitFileState " + name
}

func TestServiceExecutor_StartStoppedService(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(false, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "running",
		"enabled": "true",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl start nginx") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'systemctl start nginx' call, got %v", runner.calls)
	}
}

func TestServiceExecutor_AlreadyRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(true, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "running",
		"enabled": "true",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestServiceExecutor_StopRunningService(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(true, false)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "stopped",
		"enabled": "false",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl stop nginx") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'systemctl stop nginx' call, got %v", runner.calls)
	}
}

func TestServiceExecutor_Restart(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(true, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "restarted",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl restart nginx") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'systemctl restart nginx' call, got %v", runner.calls)
	}
}

func TestServiceExecutor_Reload(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(true, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "reloaded",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl reload nginx") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'systemctl reload nginx' call, got %v", runner.calls)
	}
}

func TestServiceExecutor_EnableDisabled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(true, false)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "running",
		"enabled": "true",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl enable nginx") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'systemctl enable nginx' call, got %v", runner.calls)
	}
}

func TestServiceExecutor_DisableEnabled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(false, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "stopped",
		"enabled": "false",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	foundDisable := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl disable nginx") {
			foundDisable = true
		}
	}
	if !foundDisable {
		t.Errorf("expected 'systemctl disable nginx' call, got %v", runner.calls)
	}
}

func TestServiceExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(false, false)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "running",
		"enabled": "true",
	}, true)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl start") || strings.Contains(c, "systemctl enable") {
			t.Errorf("dry-run should not execute state-changing commands, got: %s", c)
		}
	}
}

func TestServiceExecutor_ResourceType(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(true, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "running",
	}, false)

	if result.ResourceType != "service" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "service")
	}
}

func TestServiceExecutor_MissingName(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"state": "running",
	}, false)

	if result.Error == nil {
		t.Error("expected error for missing name, got nil")
	}
}

func TestServiceExecutor_MultipleServices(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	// nginx is stopped+disabled, php-fpm is stopped+disabled, redis is running+enabled
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(false, false)}
	runner.results[showKey("php-fpm")] = mockCmdResult{output: showOutput(false, false)}
	runner.results[showKey("redis")] = mockCmdResult{output: showOutput(true, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx, php-fpm, redis",
		"state":   "running",
		"enabled": "true",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}

	// nginx and php-fpm should be started and enabled, redis already fine
	startCount := 0
	enableCount := 0
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl start") {
			startCount++
		}
		if strings.Contains(c, "systemctl enable") {
			enableCount++
		}
	}
	if startCount != 2 {
		t.Errorf("expected 2 start calls, got %d (calls: %v)", startCount, runner.calls)
	}
	if enableCount != 2 {
		t.Errorf("expected 2 enable calls, got %d (calls: %v)", enableCount, runner.calls)
	}
}

func TestServiceExecutor_MultipleAllRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(true, true)}
	runner.results[showKey("redis")] = mockCmdResult{output: showOutput(true, true)}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":    "nginx, redis",
		"state":   "running",
		"enabled": "true",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Changed {
		t.Error("Changed = true, want false (all already running and enabled)")
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestServiceExecutor_MultipleStopsOnError(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results[showKey("nginx")] = mockCmdResult{output: showOutput(false, true)}
	runner.results["systemctl start nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1"), output: []byte("Failed to start")}

	se := &ServiceExecutor{Runner: runner}
	result := se.Execute(context.Background(), map[string]string{
		"name":  "nginx, redis",
		"state": "running",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
}
