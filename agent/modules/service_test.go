package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Compile-time interface check
var _ Executor = (*ServiceExecutor)(nil)

func TestServiceExecutor_StartStoppedService(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{err: fmt.Errorf("exit status 3")}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{err: fmt.Errorf("exit status 3")}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{err: fmt.Errorf("exit status 3")}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}

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
	runner.results["systemctl is-active --quiet nginx"] = mockCmdResult{}
	runner.results["systemctl is-enabled --quiet nginx"] = mockCmdResult{}

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
