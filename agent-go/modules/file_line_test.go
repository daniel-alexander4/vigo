// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Compile-time check that FileLineExecutor implements Executor.
var _ Executor = (*FileLineExecutor)(nil)

func TestFileLineAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "line1\nline2\nline3\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path": path,
		"line": "line2",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("changed = true, want false")
	}
}

func TestFileLineAppended(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "line1\nline2\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path": path,
		"line": "line3",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("changed = false, want true")
	}

	got := readFile(t, path)
	want := "line1\nline2\nline3\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileLineInsertedAtBeginning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "line1\nline2\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":     path,
		"line":     "line0",
		"position": "begin",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	got := readFile(t, path)
	want := "line0\nline1\nline2\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileLineMatchRegexReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "port=8080\nhost=localhost\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"line":  "port=9090",
		"match": "^port=",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("changed = false, want true")
	}

	got := readFile(t, path)
	want := "port=9090\nhost=localhost\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileLineMatchRegexAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "port=9090\nhost=localhost\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"line":  "port=9090",
		"match": "^port=",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("changed = true, want false")
	}
}

func TestFileLineAbsentPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "line1\nline2\nline3\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"line":  "line2",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("changed = false, want true")
	}

	got := readFile(t, path)
	want := "line1\nline3\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileLineAbsentNotPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "line1\nline2\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"line":  "line99",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("changed = true, want false")
	}
}

func TestFileLineFileNotExistPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.conf")

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path": path,
		"line": "newline",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("changed = false, want true")
	}

	got := readFile(t, path)
	want := "newline\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileLineFileNotExistAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.conf")

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"line":  "anything",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("changed = true, want false")
	}
	if fileExists(path) {
		t.Error("file should not have been created")
	}
}

func TestFileLineDryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	original := "line1\nline2\n"
	writeFile(t, path, original, 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path": path,
		"line": "line3",
	}, true)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("changed = false, want true")
	}

	// File should not have been modified.
	got := readFile(t, path)
	if got != original {
		t.Errorf("dry run modified file: content = %q, want %q", got, original)
	}
}

func TestFileLineMissingPath(t *testing.T) {
	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"line": "hello",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing path, got nil")
	}
}

func TestFileLineAfterPatternInsertion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "[section1]\nkey1=val1\n[section2]\nkey2=val2\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":     path,
		"line":     "key1b=val1b",
		"position": `after:^\[section1\]`,
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	got := readFile(t, path)
	want := "[section1]\nkey1b=val1b\nkey1=val1\n[section2]\nkey2=val2\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileLineResourceType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "line1\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path": path,
		"line": "line1",
	}, false)

	if result.ResourceType != "file_line" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "file_line")
	}
}

func TestFileLineDryRunFileNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.conf")

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path": path,
		"line": "newline",
	}, true)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	// File should NOT exist after dry-run.
	if fileExists(path) {
		t.Error("file was created during dry-run, should not have been")
	}
}

func TestFileLineAbsentWithMatchRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	writeFile(t, path, "port=8080\nhost=localhost\nport=9090\n", 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"match": "^port=",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("action = %q, want %q", result.Action, ActionDeleted)
	}

	got := readFile(t, path)
	want := "host=localhost\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileLinePreservesNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	// File without trailing newline — absent removal should still work.
	os.WriteFile(path, []byte("line1\nline2"), 0644)

	ex := &FileLineExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"line":  "line1",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("action = %q, want %q", result.Action, ActionDeleted)
	}

	got := readFile(t, path)
	// Original had no trailing newline, result should preserve that.
	want := "line2"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}
