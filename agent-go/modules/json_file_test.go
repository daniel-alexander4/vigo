// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"path/filepath"
	"testing"
)

var _ Executor = (*JSONFileExecutor)(nil)

func TestJSONFileSetTopLevelKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"port": 8080, "host": "localhost"}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "port",
		"value": "9090",
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

	// Re-read and verify.
	doc := readJSONHelper(t, path)
	if v, ok := doc["port"]; !ok || v != float64(9090) {
		t.Errorf("port = %v, want 9090", v)
	}
}

func TestJSONFileAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"port": 9090}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "port",
		"value": "9090",
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

func TestJSONFileSetNestedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"database": {"host": "localhost", "port": 5432}}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "database.port",
		"value": "5433",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	doc := readJSONHelper(t, path)
	db, ok := doc["database"].(map[string]any)
	if !ok {
		t.Fatal("database is not a map")
	}
	if db["port"] != float64(5433) {
		t.Errorf("database.port = %v, want 5433", db["port"])
	}
}

func TestJSONFileCreateNewKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"existing": "value"}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "newkey",
		"value": `"newval"`,
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	doc := readJSONHelper(t, path)
	if doc["newkey"] != "newval" {
		t.Errorf("newkey = %v, want %q", doc["newkey"], "newval")
	}
}

func TestJSONFileCreateNestedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "database.host",
		"value": `"localhost"`,
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	doc := readJSONHelper(t, path)
	db, ok := doc["database"].(map[string]any)
	if !ok {
		t.Fatal("database was not created as a map")
	}
	if db["host"] != "localhost" {
		t.Errorf("database.host = %v, want %q", db["host"], "localhost")
	}
}

func TestJSONFileCreateFromScratch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.json")

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "app.name",
		"value": `"hobnob"`,
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	doc := readJSONHelper(t, path)
	app, ok := doc["app"].(map[string]any)
	if !ok {
		t.Fatal("app was not created")
	}
	if app["name"] != "hobnob" {
		t.Errorf("app.name = %v, want %q", app["name"], "hobnob")
	}
}

func TestJSONFileDeleteKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"keep": "yes", "remove": "me"}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "remove",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("action = %q, want %q", result.Action, ActionDeleted)
	}

	doc := readJSONHelper(t, path)
	if _, ok := doc["remove"]; ok {
		t.Error("key 'remove' should have been deleted")
	}
	if doc["keep"] != "yes" {
		t.Errorf("keep = %v, want %q", doc["keep"], "yes")
	}
}

func TestJSONFileDeleteNestedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"database": {"host": "localhost", "port": 5432}}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "database.port",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("action = %q, want %q", result.Action, ActionDeleted)
	}

	doc := readJSONHelper(t, path)
	db := doc["database"].(map[string]any)
	if _, ok := db["port"]; ok {
		t.Error("database.port should have been deleted")
	}
	if db["host"] != "localhost" {
		t.Error("database.host should be preserved")
	}
}

func TestJSONFileDeleteKeyNotPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"existing": "value"}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "nonexistent",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("action = %q, want %q", result.Action, ActionNone)
	}
}

func TestJSONFileDeleteFileNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")

	ex := &JSONFileExecutor{}
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

func TestJSONFileDryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	original := `{"port": 8080}`
	writeFile(t, path, original, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "port",
		"value": "9090",
	}, true)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	got := readFile(t, path)
	if got != original {
		t.Errorf("dry run modified file: got %q, want %q", got, original)
	}
}

func TestJSONFileMissingPath(t *testing.T) {
	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"key":   "k",
		"value": `"v"`,
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestJSONFileMissingKey(t *testing.T) {
	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  "/tmp/test.json",
		"value": `"v"`,
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestJSONFileInvalidValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "k",
		"value": "not valid json{",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for invalid JSON value")
	}
}

func TestJSONFileResourceType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"k": "v"}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "k",
		"value": `"v"`,
	}, false)

	if result.ResourceType != "json_file" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "json_file")
	}
}

func TestJSONFileSetBoolValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"debug": false}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "debug",
		"value": "true",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	doc := readJSONHelper(t, path)
	if doc["debug"] != true {
		t.Errorf("debug = %v, want true", doc["debug"])
	}
}

func TestJSONFileSetObjectValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "logging",
		"value": `{"level": "info", "file": "/var/log/app.log"}`,
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	doc := readJSONHelper(t, path)
	logging, ok := doc["logging"].(map[string]any)
	if !ok {
		t.Fatal("logging is not a map")
	}
	if logging["level"] != "info" {
		t.Errorf("logging.level = %v, want %q", logging["level"], "info")
	}
}

func TestJSONFileSetArrayElement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	writeFile(t, path, `{"servers": ["web1", "web2", "web3"]}`, 0644)

	ex := &JSONFileExecutor{}
	result := ex.Execute(context.Background(), map[string]string{
		"path":  path,
		"key":   "servers.1",
		"value": `"web2-updated"`,
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, ActionUpdated)
	}

	doc := readJSONHelper(t, path)
	servers := doc["servers"].([]any)
	if servers[1] != "web2-updated" {
		t.Errorf("servers[1] = %v, want %q", servers[1], "web2-updated")
	}
}

// readJSONHelper reads and parses a JSON file for test assertions.
func readJSONHelper(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, err := readJSONFile(path)
	if err != nil {
		t.Fatalf("failed to read JSON file: %v", err)
	}
	return doc
}
