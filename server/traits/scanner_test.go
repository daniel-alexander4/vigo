package traits

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScanDir_FiltersExecutables(t *testing.T) {
	dir := t.TempDir()

	// Create 2 executable files and 1 non-executable.
	writeExec(t, dir, "app-version.sh", "#!/bin/sh\necho hello")
	writeExec(t, dir, "disk-check", "#!/bin/sh\ndf -h")
	writeFile(t, dir, "README.md", "this is a readme")

	scanner := NewScanner(dir, 0)
	entries, err := scanner.ScanDir()
	if err != nil {
		t.Fatalf("ScanDir() error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Sorted by name: app-version before disk-check.
	if entries[0].Name != "app-version" {
		t.Errorf("entries[0].Name = %q, want %q", entries[0].Name, "app-version")
	}
	if entries[1].Name != "disk-check" {
		t.Errorf("entries[1].Name = %q, want %q", entries[1].Name, "disk-check")
	}

	// Verify checksums.
	wantHash0 := sha256Hex([]byte("#!/bin/sh\necho hello"))
	if entries[0].SHA256 != wantHash0 {
		t.Errorf("entries[0].SHA256 = %q, want %q", entries[0].SHA256, wantHash0)
	}
	wantHash1 := sha256Hex([]byte("#!/bin/sh\ndf -h"))
	if entries[1].SHA256 != wantHash1 {
		t.Errorf("entries[1].SHA256 = %q, want %q", entries[1].SHA256, wantHash1)
	}
}

func TestScanDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	scanner := NewScanner(dir, 0)
	entries, err := scanner.ScanDir()
	if err != nil {
		t.Fatalf("ScanDir() error: %v", err)
	}

	if entries != nil {
		t.Errorf("expected nil for empty dir, got %v", entries)
	}
}

func TestScanDir_NonExistentDir(t *testing.T) {
	scanner := NewScanner("/nonexistent/path/that/does/not/exist", 0)
	entries, err := scanner.ScanDir()
	if err != nil {
		t.Fatalf("ScanDir() error: %v (expected nil error for missing dir)", err)
	}

	if entries != nil {
		t.Errorf("expected nil for non-existent dir, got %v", entries)
	}
}

func TestScanDir_NameDerivation(t *testing.T) {
	dir := t.TempDir()

	writeExec(t, dir, "app-version.sh", "v1")
	writeExec(t, dir, "disk-check", "ok")
	writeExec(t, dir, "memory.py", "mem")
	writeExec(t, dir, "cpu.check.sh", "cpu")

	scanner := NewScanner(dir, 0)
	entries, err := scanner.ScanDir()
	if err != nil {
		t.Fatalf("ScanDir() error: %v", err)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}

	want := []string{"app-version", "cpu.check", "disk-check", "memory"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("expected trait name %q in results", w)
		}
	}
}

func TestScanDir_SkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()

	writeExec(t, dir, "top-level.sh", "data")
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, subdir, "nested.sh", "nested")

	scanner := NewScanner(dir, 0)
	entries, err := scanner.ScanDir()
	if err != nil {
		t.Fatalf("ScanDir() error: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (flat scan), got %d", len(entries))
	}
	if entries[0].Name != "top-level" {
		t.Errorf("entries[0].Name = %q, want %q", entries[0].Name, "top-level")
	}
}

func TestReadFile_Existing(t *testing.T) {
	dir := t.TempDir()

	content := "#!/bin/sh\necho hello"
	writeExec(t, dir, "app-version.sh", content)

	scanner := NewScanner(dir, 0)
	// Must call ScanDir first to build name mapping.
	if _, err := scanner.ScanDir(); err != nil {
		t.Fatal(err)
	}

	data, err := scanner.ReadFile("app-version")
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(data) != content {
		t.Errorf("ReadFile() = %q, want %q", string(data), content)
	}
}

func TestReadFile_NonExistent(t *testing.T) {
	dir := t.TempDir()

	scanner := NewScanner(dir, 0)
	if _, err := scanner.ScanDir(); err != nil {
		t.Fatal(err)
	}

	_, err := scanner.ReadFile("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent trait, got nil")
	}
}

func TestReadFile_WithoutExtension(t *testing.T) {
	dir := t.TempDir()

	content := "#!/bin/sh\ndf -h"
	writeExec(t, dir, "disk-check", content)

	scanner := NewScanner(dir, 0)
	if _, err := scanner.ScanDir(); err != nil {
		t.Fatal(err)
	}

	data, err := scanner.ReadFile("disk-check")
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if string(data) != content {
		t.Errorf("ReadFile() = %q, want %q", string(data), content)
	}
}

// writeExec creates a file with the executable bit set.
func writeExec(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeFile creates a non-executable file.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sha256Hex computes the hex-encoded SHA256 checksum of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
