// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCustomExecutor_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "test-exec")
	err := os.WriteFile(script, []byte(`#!/bin/sh
read input
echo '{"action": "created", "changed": true, "error": ""}'
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	e := &CustomExecutor{ScriptPath: script}
	result := e.Execute(context.Background(), map[string]string{"name": "test"}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("expected changed=true")
	}
}

func TestCustomExecutor_DryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	dir := t.TempDir()
	// Script reads input and echoes dry_run value from the input.
	script := filepath.Join(dir, "test-exec")
	err := os.WriteFile(script, []byte(`#!/bin/sh
read input
echo '{"action": "already in desired state", "changed": false, "error": ""}'
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	e := &CustomExecutor{ScriptPath: script}
	result := e.Execute(context.Background(), map[string]string{"name": "test"}, true)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("expected changed=false")
	}
}

func TestCustomExecutor_ScriptError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "test-exec")
	err := os.WriteFile(script, []byte(`#!/bin/sh
read input
echo '{"action": "", "changed": false, "error": "something went wrong"}'
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	e := &CustomExecutor{ScriptPath: script}
	result := e.Execute(context.Background(), map[string]string{"name": "test"}, false)

	if result.Error == nil {
		t.Fatal("expected error")
	}
	if result.Error.Error() != "something went wrong" {
		t.Errorf("error = %q, want 'something went wrong'", result.Error)
	}
}

func TestCustomExecutor_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "test-exec")
	err := os.WriteFile(script, []byte(`#!/bin/sh
echo "crash" >&2
exit 1
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	e := &CustomExecutor{ScriptPath: script}
	result := e.Execute(context.Background(), map[string]string{"name": "test"}, false)

	if result.Error == nil {
		t.Fatal("expected error for non-zero exit")
	}
}

func TestCustomExecutor_InvalidJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "test-exec")
	err := os.WriteFile(script, []byte(`#!/bin/sh
echo 'not json'
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	e := &CustomExecutor{ScriptPath: script}
	result := e.Execute(context.Background(), map[string]string{"name": "test"}, false)

	if result.Error == nil {
		t.Fatal("expected error for invalid JSON output")
	}
}

func TestCustomExecutor_EmptyAction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "test-exec")
	err := os.WriteFile(script, []byte(`#!/bin/sh
read input
echo '{"changed": false}'
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	e := &CustomExecutor{ScriptPath: script}
	result := e.Execute(context.Background(), map[string]string{"name": "test"}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("empty action should default to %q, got %q", ActionNone, result.Action)
	}
}
