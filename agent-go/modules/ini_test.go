// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"path/filepath"
	"testing"
)

var _ Executor = (*INIExecutor)(nil)

func TestINISetKeyGlobalSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "key1=value1\nkey2=value2\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "key1",
		"value": "newvalue",
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
	want := "key1=newvalue\nkey2=value2\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINISetKeyInSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[database]\nhost=localhost\nport=5432\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "database",
		"key":     "port",
		"value":   "5433",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	got := readFile(t, path)
	want := "[database]\nhost=localhost\nport=5433\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINIAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[app]\nport=8080\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "app",
		"key":     "port",
		"value":   "8080",
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

func TestINICreateNewKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[database]\nhost=localhost\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "database",
		"key":     "port",
		"value":   "5432",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	got := readFile(t, path)
	want := "[database]\nhost=localhost\nport=5432\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINICreateNewSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[existing]\nfoo=bar\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "newsection",
		"key":     "newkey",
		"value":   "newval",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	got := readFile(t, path)
	want := "[existing]\nfoo=bar\n[newsection]\nnewkey=newval\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINICreateFileFromScratch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.ini")

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "app",
		"key":     "port",
		"value":   "3000",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	got := readFile(t, path)
	want := "[app]\nport=3000\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINIRemoveKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[database]\nhost=localhost\nport=5432\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "database",
		"key":     "port",
		"state":   "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("action = %q, want %q", result.Action, ActionDeleted)
	}

	got := readFile(t, path)
	want := "[database]\nhost=localhost\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINIRemoveKeyNotPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[database]\nhost=localhost\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "database",
		"key":     "nonexistent",
		"state":   "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
}

func TestINIRemoveKeyFileNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.ini")

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "anything",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
}

func TestINIDryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	original := "[app]\nport=3000\n"
	writeFile(t, path, original, 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "app",
		"key":     "port",
		"value":   "4000",
	}, true)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	// File should not have changed.
	got := readFile(t, path)
	if got != original {
		t.Errorf("dry run modified file: got %q, want %q", got, original)
	}
}

func TestINIPreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "# Main config\n[database]\n; DB host\nhost=localhost\nport=5432\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "database",
		"key":     "port",
		"value":   "5433",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	got := readFile(t, path)
	want := "# Main config\n[database]\n; DB host\nhost=localhost\nport=5433\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINICustomSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[section]\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":      path,
		"section":   "section",
		"key":       "key",
		"value":     "value",
		"separator": " = ",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	got := readFile(t, path)
	want := "[section]\nkey = value\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINIMissingPath(t *testing.T) {
	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"key":   "k",
		"value": "v",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestINIMissingKey(t *testing.T) {
	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  "/tmp/test.ini",
		"value": "v",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestINIResourceType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "key=val\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "key",
		"value": "val",
	}, false)

	if result.ResourceType != "ini" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "ini")
	}
}

func TestINIMultipleSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "[section1]\nkey=val1\n[section2]\nkey=val2\n", 0644)

	ex := &INIExecutor{}
	// Update key in section2 only.
	result := ex.Execute(context.Background(), map[string]string{
		"path":    path,
		"section": "section2",
		"key":     "key",
		"value":   "updated",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	got := readFile(t, path)
	want := "[section1]\nkey=val1\n[section2]\nkey=updated\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestINIGlobalKeyWithSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	writeFile(t, path, "global_key=old\n[section1]\nkey=val\n", 0644)

	ex := &INIExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "global_key",
		"value": "new",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	got := readFile(t, path)
	want := "global_key=new\n[section1]\nkey=val\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}
