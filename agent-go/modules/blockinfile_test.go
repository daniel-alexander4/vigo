// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

var _ Executor = (*BlockinfileExecutor)(nil)

func TestBlockinfileExecutor_Present_InsertsBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, "line1\nline2\n", 0644)

	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"path":  path,
		"block": "managed line 1\nmanaged line 2",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}

	content := readFile(t, path)
	if !strings.Contains(content, "# BEGIN HOBNOB MANAGED BLOCK myblock") {
		t.Error("should contain begin marker")
	}
	if !strings.Contains(content, "managed line 1") {
		t.Error("should contain block content")
	}
	if !strings.Contains(content, "# END HOBNOB MANAGED BLOCK myblock") {
		t.Error("should contain end marker")
	}
	if !strings.Contains(content, "line1") {
		t.Error("should preserve original content")
	}
}

func TestBlockinfileExecutor_Present_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, "line1\n", 0644)

	e := &BlockinfileExecutor{}
	params := map[string]string{
		"name":  "myblock",
		"path":  path,
		"block": "managed content",
	}

	e.Execute(context.Background(), params, false)
	result := e.Execute(context.Background(), params, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestBlockinfileExecutor_Present_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	content := "before\n# BEGIN HOBNOB MANAGED BLOCK myblock\nold content\n# END HOBNOB MANAGED BLOCK myblock\nafter\n"
	writeFile(t, path, content, 0644)

	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"path":  path,
		"block": "new content",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}

	newContent := readFile(t, path)
	if strings.Contains(newContent, "old content") {
		t.Error("should not contain old content")
	}
	if !strings.Contains(newContent, "new content") {
		t.Error("should contain new content")
	}
	if !strings.Contains(newContent, "before") {
		t.Error("should preserve content before block")
	}
	if !strings.Contains(newContent, "after") {
		t.Error("should preserve content after block")
	}
}

func TestBlockinfileExecutor_Absent_RemovesBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	content := "before\n# BEGIN HOBNOB MANAGED BLOCK myblock\nmanaged content\n# END HOBNOB MANAGED BLOCK myblock\nafter\n"
	writeFile(t, path, content, 0644)

	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"path":  path,
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}

	newContent := readFile(t, path)
	if strings.Contains(newContent, "MANAGED BLOCK") {
		t.Error("should not contain markers")
	}
	if strings.Contains(newContent, "managed content") {
		t.Error("should not contain block content")
	}
	if !strings.Contains(newContent, "before") {
		t.Error("should preserve content before block")
	}
	if !strings.Contains(newContent, "after") {
		t.Error("should preserve content after block")
	}
}

func TestBlockinfileExecutor_Absent_NoBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, "no block here\n", 0644)

	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"path":  path,
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestBlockinfileExecutor_Absent_FileNotFound(t *testing.T) {
	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"path":  "/nonexistent/file",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestBlockinfileExecutor_Present_CreateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "newfile")

	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":   "myblock",
		"path":   path,
		"block":  "new content",
		"create": "true",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	content := readFile(t, path)
	if !strings.Contains(content, "new content") {
		t.Error("should contain block content")
	}
}

func TestBlockinfileExecutor_Present_FileNotFound_NoCreate(t *testing.T) {
	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"path":  "/nonexistent/file",
		"block": "content",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error when file not found and create=false")
	}
}

func TestBlockinfileExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, "original\n", 0644)

	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"path":  path,
		"block": "new content",
	}, true)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}

	content := readFile(t, path)
	if strings.Contains(content, "new content") {
		t.Error("dry-run should not modify file")
	}
}

func TestBlockinfileExecutor_MissingPath(t *testing.T) {
	e := &BlockinfileExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myblock",
		"block": "content",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestBlockinfileExecutor_CustomMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, "line1\n", 0644)

	e := &BlockinfileExecutor{}
	e.Execute(context.Background(), map[string]string{
		"name":         "myblock",
		"path":         path,
		"block":        "custom content",
		"marker_begin": "<!-- BEGIN",
		"marker_end":   "<!-- END",
	}, false)

	content := readFile(t, path)
	if !strings.Contains(content, "<!-- BEGIN myblock") {
		t.Error("should use custom begin marker")
	}
	if !strings.Contains(content, "<!-- END myblock") {
		t.Error("should use custom end marker")
	}
}
