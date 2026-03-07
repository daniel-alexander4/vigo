package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Compile-time check that ArchiveExecutor implements Executor.
var _ Executor = (*ArchiveExecutor)(nil)

func testSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestArchiveExecutor_DirectBinary(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho hello\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binaryContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")

	exe := &ArchiveExecutor{HTTPClient: srv.Client()}
	result := exe.Execute(context.Background(), map[string]string{
		"url":  srv.URL + "/mybinary",
		"path": path,
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.ResourceType != "archive" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "archive")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(binaryContent) {
		t.Errorf("content = %q, want %q", got, binaryContent)
	}

	// Check mode is 0755 by default.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("mode = %o, want 0755", info.Mode().Perm())
	}
}

func TestArchiveExecutor_IdempotentFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")
	os.WriteFile(path, []byte("existing"), 0755)

	exe := &ArchiveExecutor{}
	result := exe.Execute(context.Background(), map[string]string{
		"url":  "https://example.com/binary",
		"path": path,
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (file exists, no hash/unless)", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestArchiveExecutor_SHA256Match(t *testing.T) {
	content := []byte("correct binary")
	hash := testSHA256(content)

	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")
	os.WriteFile(path, content, 0755)

	exe := &ArchiveExecutor{}
	result := exe.Execute(context.Background(), map[string]string{
		"url":    "https://example.com/binary",
		"path":   path,
		"sha256": hash,
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (hash matches)", result.Action, ActionNone)
	}
}

func TestArchiveExecutor_SHA256Mismatch(t *testing.T) {
	newContent := []byte("updated binary")
	newHash := testSHA256(newContent)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")
	os.WriteFile(path, []byte("old binary"), 0755)

	exe := &ArchiveExecutor{HTTPClient: srv.Client()}
	result := exe.Execute(context.Background(), map[string]string{
		"url":    srv.URL + "/binary",
		"path":   path,
		"sha256": newHash,
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q (hash mismatch triggers update)", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
}

func TestArchiveExecutor_SHA256VerifyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("wrong content"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")

	exe := &ArchiveExecutor{HTTPClient: srv.Client()}
	result := exe.Execute(context.Background(), map[string]string{
		"url":    srv.URL + "/binary",
		"path":   path,
		"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want hash mismatch error")
	}
	// Binary should have been removed after hash mismatch.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("binary should be removed after hash mismatch")
	}
}

func TestArchiveExecutor_UnlessSkips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")
	os.WriteFile(path, []byte("binary"), 0755)

	runner := newMockCmdRunnerGeneric()
	runner.results["true"] = mockCmdResult{output: []byte(""), err: nil}

	exe := &ArchiveExecutor{Runner: runner}
	result := exe.Execute(context.Background(), map[string]string{
		"url":    "https://example.com/binary",
		"path":   path,
		"unless": "true",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (unless succeeded)", result.Action, ActionNone)
	}
}

func TestArchiveExecutor_UnlessFailsTriggersUpdate(t *testing.T) {
	binaryContent := []byte("new version")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binaryContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")
	os.WriteFile(path, []byte("old version"), 0755)

	runner := newMockCmdRunnerGeneric()
	runner.results["glow --version"] = mockCmdResult{output: nil, err: fmt.Errorf("exit 1")}

	exe := &ArchiveExecutor{Runner: runner, HTTPClient: srv.Client()}
	result := exe.Execute(context.Background(), map[string]string{
		"url":    srv.URL + "/binary",
		"path":   path,
		"unless": "glow --version",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestArchiveExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")

	exe := &ArchiveExecutor{}
	result := exe.Execute(context.Background(), map[string]string{
		"url":  "https://example.com/binary",
		"path": path,
	}, true)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("binary should not be created during dry-run")
	}
}

func TestArchiveExecutor_Absent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")
	os.WriteFile(path, []byte("to delete"), 0755)

	exe := &ArchiveExecutor{}
	result := exe.Execute(context.Background(), map[string]string{
		"url":   "https://example.com/binary",
		"path":  path,
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("binary should be removed")
	}
}

func TestArchiveExecutor_AbsentNotPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent")

	exe := &ArchiveExecutor{}
	result := exe.Execute(context.Background(), map[string]string{
		"url":   "https://example.com/binary",
		"path":  path,
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestArchiveExecutor_MissingURL(t *testing.T) {
	exe := &ArchiveExecutor{}
	result := exe.Execute(context.Background(), map[string]string{
		"path": "/usr/local/bin/something",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error for missing url")
	}
}

func TestArchiveExecutor_MissingPath(t *testing.T) {
	exe := &ArchiveExecutor{}
	result := exe.Execute(context.Background(), map[string]string{
		"url": "https://example.com/binary",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error for missing path")
	}
}

func TestArchiveExecutor_CustomMode(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho hello\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binaryContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")

	exe := &ArchiveExecutor{HTTPClient: srv.Client()}
	result := exe.Execute(context.Background(), map[string]string{
		"url":  srv.URL + "/mybinary",
		"path": path,
		"mode": "0700",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestArchiveExecutor_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")

	exe := &ArchiveExecutor{HTTPClient: srv.Client()}
	result := exe.Execute(context.Background(), map[string]string{
		"url":  srv.URL + "/notfound",
		"path": path,
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error for HTTP 404")
	}
}

func TestArchiveExecutor_CreatesParentDir(t *testing.T) {
	binaryContent := []byte("binary")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binaryContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "mybinary")

	exe := &ArchiveExecutor{HTTPClient: srv.Client()}
	result := exe.Execute(context.Background(), map[string]string{
		"url":  srv.URL + "/binary",
		"path": path,
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("binary not created at nested path: %v", err)
	}
}

func TestIsArchive(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.com/bin.tar.gz", true},
		{"https://example.com/bin.tgz", true},
		{"https://example.com/bin.tar.bz2", true},
		{"https://example.com/bin.tar.xz", true},
		{"https://example.com/bin.zip", true},
		{"https://example.com/pkg.deb", true},
		{"https://example.com/pkg.rpm", true},
		{"https://example.com/binary", false},
		{"https://example.com/file.txt", false},
	}

	for _, tt := range tests {
		if got := isArchive(tt.url); got != tt.want {
			t.Errorf("isArchive(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestFindBinary(t *testing.T) {
	dir := t.TempDir()

	// Create a nested structure with a binary.
	binDir := filepath.Join(dir, "glow_1.0")
	os.MkdirAll(binDir, 0755)
	binPath := filepath.Join(binDir, "glow")
	os.WriteFile(binPath, []byte("binary"), 0755)
	// Also create a README (not executable).
	os.WriteFile(filepath.Join(binDir, "README.md"), []byte("docs"), 0644)

	// Find by exact name.
	found, err := findBinary(dir, "glow", "")
	if err != nil {
		t.Fatalf("findBinary by name: %v", err)
	}
	if found != binPath {
		t.Errorf("found = %q, want %q", found, binPath)
	}

	// Find by fallback name.
	found, err = findBinary(dir, "", "glow")
	if err != nil {
		t.Fatalf("findBinary by fallback: %v", err)
	}
	if found != binPath {
		t.Errorf("found = %q, want %q", found, binPath)
	}

	// Find first executable.
	found, err = findBinary(dir, "nonexistent", "alsonothere")
	if err != nil {
		t.Fatalf("findBinary first exec: %v", err)
	}
	if found != binPath {
		t.Errorf("found = %q, want %q", found, binPath)
	}
}
