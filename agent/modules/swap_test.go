package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var _ Executor = (*SwapExecutor)(nil)

func TestSwapExecutor_Present_CreatesNewSwap(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, fstab, "", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["fallocate -l 1024M "+swapPath] = mockCmdResult{}
	runner.results["chmod 0600 "+swapPath] = mockCmdResult{}
	runner.results["mkswap "+swapPath] = mockCmdResult{}
	runner.results["swapon "+swapPath] = mockCmdResult{}

	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path": swapPath,
		"size": "1024",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	// Verify fstab entry.
	content := readFile(t, fstab)
	if !strings.Contains(content, swapPath) {
		t.Errorf("fstab should contain %s, got: %s", swapPath, content)
	}
	if !strings.Contains(content, "swap") {
		t.Error("fstab entry should contain 'swap'")
	}
}

func TestSwapExecutor_Present_AlreadyActive(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, swapPath, "swapdata", 0600)
	writeFile(t, fstab, swapPath+"\tnone\tswap\tsw\t0\t0\n", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n"+swapPath+"\tfile\t1048572\t0\t-2\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path": swapPath,
		"size": "1024",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestSwapExecutor_Present_WithPriority(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, fstab, "", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["fallocate -l 512M "+swapPath] = mockCmdResult{}
	runner.results["chmod 0600 "+swapPath] = mockCmdResult{}
	runner.results["mkswap "+swapPath] = mockCmdResult{}
	runner.results["swapon -p 10 "+swapPath] = mockCmdResult{}

	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path":     swapPath,
		"size":     "512",
		"priority": "10",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	content := readFile(t, fstab)
	if !strings.Contains(content, "pri=10") {
		t.Errorf("fstab should contain pri=10, got: %s", content)
	}

	// Verify swapon called with -p flag.
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "swapon -p 10") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected swapon -p 10 call, got %v", runner.calls)
	}
}

func TestSwapExecutor_Absent_RemovesSwap(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, swapPath, "swapdata", 0600)
	writeFile(t, fstab, "# comment\n"+swapPath+"\tnone\tswap\tsw\t0\t0\n", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n"+swapPath+"\tfile\t1048572\t0\t-2\n", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["swapoff "+swapPath] = mockCmdResult{}

	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path":  swapPath,
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	// File should be removed.
	if fileExists(swapPath) {
		t.Error("swap file should be removed")
	}

	// Should be removed from fstab.
	content := readFile(t, fstab)
	if strings.Contains(content, swapPath) {
		t.Errorf("fstab should not contain %s, got: %s", swapPath, content)
	}
	// Comment should be preserved.
	if !strings.Contains(content, "# comment") {
		t.Error("fstab comments should be preserved")
	}
}

func TestSwapExecutor_Absent_AlreadyAbsent(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")

	writeFile(t, fstab, "", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path":  "/nonexistent/swap",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestSwapExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, fstab, "", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path": swapPath,
		"size": "1024",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	// No commands should have been run.
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not run commands, got %v", runner.calls)
	}
	// Fstab should not be modified.
	content := readFile(t, fstab)
	if strings.Contains(content, swapPath) {
		t.Error("dry-run should not modify fstab")
	}
}

func TestSwapExecutor_MissingPath(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"size": "1024",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(result.Error.Error(), "path or name") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestSwapExecutor_MissingSizeForPresent(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path": "/swap",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing size")
	}
	if !strings.Contains(result.Error.Error(), "size param is required") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestSwapExecutor_UnknownState(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path":  "/swap",
		"state": "maybe",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for unknown state")
	}
	if !strings.Contains(result.Error.Error(), "unknown state") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestSwapExecutor_FallocateFailure(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, fstab, "", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["fallocate -l 1024M "+swapPath] = mockCmdResult{
		err: fmt.Errorf("no space left"),
	}

	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path": swapPath,
		"size": "1024",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error from fallocate failure")
	}
	if !strings.Contains(result.Error.Error(), "fallocate") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestSwapExecutor_NameFallback(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, swapPath, "swapdata", 0600)
	writeFile(t, fstab, swapPath+"\tnone\tswap\tsw\t0\t0\n", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n"+swapPath+"\tfile\t1048572\t0\t-2\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": swapPath,
		"size": "1024",
	}, false)

	if result.ResourceName != swapPath {
		t.Errorf("ResourceName = %q, want %q", result.ResourceName, swapPath)
	}
}

func TestSwapExecutor_Present_FileExistsButNotActive(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, swapPath, "swapdata", 0600)
	writeFile(t, fstab, "", 0644)
	writeFile(t, procSwaps, "Filename\tType\tSize\tUsed\tPriority\n", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["swapon "+swapPath] = mockCmdResult{}

	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path": swapPath,
		"size": "1024",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	// Should NOT call fallocate/mkswap since file exists.
	for _, c := range runner.calls {
		if strings.Contains(c, "fallocate") || strings.Contains(c, "mkswap") {
			t.Errorf("should not recreate existing swap file, got: %s", c)
		}
	}
}

func TestSwapExecutor_Absent_SwapoffFailure(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, swapPath, "swapdata", 0600)
	writeFile(t, fstab, swapPath+"\tnone\tswap\tsw\t0\t0\n", 0644)
	writeFile(t, procSwaps, swapPath+"\tfile\t1048572\t0\t-2\n", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["swapoff "+swapPath] = mockCmdResult{err: fmt.Errorf("device busy")}

	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path":  swapPath,
		"state": "absent",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error from swapoff failure")
	}
	if !strings.Contains(result.Error.Error(), "swapoff") {
		t.Errorf("error = %v", result.Error)
	}
	// File should NOT be removed on failure.
	if !fileExists(swapPath) {
		t.Error("swap file should not be removed on swapoff failure")
	}
}

func TestSwapExecutor_Absent_DryRun(t *testing.T) {
	dir := t.TempDir()
	fstab := filepath.Join(dir, "fstab")
	procSwaps := filepath.Join(dir, "swaps")
	swapPath := filepath.Join(dir, "swapfile")

	writeFile(t, swapPath, "swapdata", 0600)
	writeFile(t, fstab, swapPath+"\tnone\tswap\tsw\t0\t0\n", 0644)
	writeFile(t, procSwaps, swapPath+"\tfile\t1048572\t0\t-2\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &SwapExecutor{FstabFile: fstab, ProcSwaps: procSwaps, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path":  swapPath,
		"state": "absent",
	}, true)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	// File should still exist.
	if !fileExists(swapPath) {
		t.Error("dry-run should not remove swap file")
	}
	// No commands should run.
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not run commands, got %v", runner.calls)
	}

	// Verify fstab not modified.
	fstabContent, err := os.ReadFile(fstab)
	if err != nil {
		t.Fatalf("reading fstab: %v", err)
	}
	if !strings.Contains(string(fstabContent), swapPath) {
		t.Error("dry-run should not modify fstab")
	}
}
