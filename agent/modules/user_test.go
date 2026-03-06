package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compile-time interface check
var _ Executor = (*UserExecutor)(nil)

func TestUserExecutor_CreateMissing(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") && strings.Contains(c, "www-data") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected useradd call, got %v", runner.calls)
	}
}

func TestUserExecutor_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestUserExecutor_UpdateAttributes(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("www-data:x:33:33:www-data:/var/www:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "usermod") && strings.Contains(c, "www-data") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected usermod call, got %v", runner.calls)
	}
}

func TestUserExecutor_RemoveExisting(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "userdel") && strings.Contains(c, "www-data") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected userdel call, got %v", runner.calls)
	}
}

func TestUserExecutor_RemoveNonexistent(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestUserExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") {
			t.Errorf("dry-run should not run useradd, got: %s", c)
		}
	}
}

func TestUserExecutor_CreateWithGroup(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "deploy",
		"group": "www-data",
		"state": "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	foundGroup := false
	for _, c := range runner.calls {
		if strings.Contains(c, "-g") && strings.Contains(c, "www-data") {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Errorf("expected -g www-data in useradd call, got %v", runner.calls)
	}
}

func TestUserExecutor_ResourceType(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "testuser",
		"state": "present",
	}, true)

	if result.ResourceType != "user" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "user")
	}
}

func TestUserExecutor_MissingName(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: "/dev/null"}
	result := ue.Execute(context.Background(), map[string]string{
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Error("expected error for missing name, got nil")
	}
}

func TestUserExecutor_UseradError(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["useradd"] = mockCmdResult{err: fmt.Errorf("exit status 1")}
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "testuser",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Error("expected error from useradd failure, got nil")
	}
}
