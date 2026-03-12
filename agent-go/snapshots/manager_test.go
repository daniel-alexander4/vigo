// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package snapshots

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSaveCreatesSnapshot(t *testing.T) {
	dir := t.TempDir()
	snapshotDir := t.TempDir()

	filePath := filepath.Join(dir, "test.conf")
	content := "original content\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{BaseDir: snapshotDir, MaxPerPath: 5}
	if err := mgr.Save(filePath, "run-001"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snaps := listSnapsForPath(t, snapshotDir, filePath)
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snaps))
	}

	// Verify snapshot content matches original
	snapData, err := os.ReadFile(filepath.Join(snapshotDir, snaps[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(snapData) != content {
		t.Errorf("snapshot content = %q, want %q", string(snapData), content)
	}
}

func TestSaveCreatesSnapshotDir(t *testing.T) {
	dir := t.TempDir()
	snapshotDir := filepath.Join(dir, "snaps", "nested")

	filePath := filepath.Join(dir, "test.conf")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{BaseDir: snapshotDir, MaxPerPath: 5}
	if err := mgr.Save(filePath, "run-001"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(snapshotDir)
	if err != nil {
		t.Fatal("snapshot dir was not created")
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("snapshot dir mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestPruneKeepsMaxPerPath(t *testing.T) {
	dir := t.TempDir()
	snapshotDir := t.TempDir()
	filePath := filepath.Join(dir, "test.conf")

	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{BaseDir: snapshotDir, MaxPerPath: 3}

	// Create 5 snapshots
	for i := 0; i < 5; i++ {
		if err := mgr.Save(filePath, fmt.Sprintf("run-%03d", i)); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	snaps := listSnapsForPath(t, snapshotDir, filePath)
	if len(snaps) != 3 {
		t.Fatalf("got %d snapshots, want 3 (max per path)", len(snaps))
	}

	// Should keep the most recent 3 (run-002, run-003, run-004)
	for _, s := range snaps {
		if strings.Contains(s, "run-000") || strings.Contains(s, "run-001") {
			t.Errorf("old snapshot %s should have been pruned", s)
		}
	}
}

func TestRestoreCopiesBack(t *testing.T) {
	dir := t.TempDir()
	snapshotDir := t.TempDir()
	filePath := filepath.Join(dir, "test.conf")
	originalContent := "original\n"

	if err := os.WriteFile(filePath, []byte(originalContent), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{BaseDir: snapshotDir, MaxPerPath: 5}
	if err := mgr.Save(filePath, "run-001"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Overwrite the file
	if err := os.WriteFile(filePath, []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Get the snapshot path
	snaps := listSnapsForPath(t, snapshotDir, filePath)
	if len(snaps) == 0 {
		t.Fatal("no snapshot found")
	}

	snapshotPath := filepath.Join(snapshotDir, snaps[0])
	if err := mgr.Restore(filePath, snapshotPath); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != originalContent {
		t.Errorf("restored content = %q, want %q", string(data), originalContent)
	}
}

func TestSaveNonExistentFile(t *testing.T) {
	snapshotDir := t.TempDir()
	mgr := &Manager{BaseDir: snapshotDir, MaxPerPath: 5}

	err := mgr.Save("/nonexistent/file.txt", "run-001")
	if err == nil {
		t.Error("Save non-existent file should return error")
	}
}

func TestSnapshotNaming(t *testing.T) {
	dir := t.TempDir()
	snapshotDir := t.TempDir()
	filePath := filepath.Join(dir, "test.conf")

	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{BaseDir: snapshotDir, MaxPerPath: 5}
	if err := mgr.Save(filePath, "run-abc"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snaps := listSnapsForPath(t, snapshotDir, filePath)
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snaps))
	}

	expectedPrefix := fmt.Sprintf("%x", sha256.Sum256([]byte(filePath)))
	expectedName := expectedPrefix + ".run-abc.bak"
	if snaps[0] != expectedName {
		t.Errorf("snapshot name = %q, want %q", snaps[0], expectedName)
	}
}

func listSnapsForPath(t *testing.T, snapshotDir, filePath string) []string {
	t.Helper()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(filePath)))
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		t.Fatalf("read snapshot dir: %v", err)
	}
	var matches []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), hash) {
			matches = append(matches, e.Name())
		}
	}
	sort.Strings(matches)
	return matches
}
