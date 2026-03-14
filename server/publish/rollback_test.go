// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package publish

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotAndRollback(t *testing.T) {
	liveDir := t.TempDir()
	historyDir := t.TempDir()

	// Create some content in live.
	modDir := filepath.Join(liveDir, "modules")
	os.MkdirAll(modDir, 0755)
	os.WriteFile(filepath.Join(modDir, "nginx.hnb"), []byte("name: nginx\nresources: []\n"), 0644)

	// Snapshot.
	version, err := SnapshotLive(liveDir, historyDir, 5)
	if err != nil {
		t.Fatalf("SnapshotLive: %v", err)
	}
	if version == "" {
		t.Fatal("expected non-empty version")
	}

	// List versions.
	versions, err := ListVersions(historyDir)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(versions))
	}
	if versions[0] != version {
		t.Errorf("version mismatch: got %s, want %s", versions[0], version)
	}

	// Modify live.
	os.WriteFile(filepath.Join(modDir, "nginx.hnb"), []byte("name: nginx\nresources: [changed]\n"), 0644)

	// Rollback.
	if err := RollbackToVersion(historyDir, version, liveDir); err != nil {
		t.Fatalf("RollbackToVersion: %v", err)
	}

	// Verify content is restored.
	data, err := os.ReadFile(filepath.Join(liveDir, "modules", "nginx.hnb"))
	if err != nil {
		t.Fatalf("reading restored file: %v", err)
	}
	if string(data) != "name: nginx\nresources: []\n" {
		t.Errorf("content not restored: %s", string(data))
	}
}

func TestSnapshotPruning(t *testing.T) {
	liveDir := t.TempDir()
	historyDir := t.TempDir()

	modDir := filepath.Join(liveDir, "modules")
	os.MkdirAll(modDir, 0755)
	os.WriteFile(filepath.Join(modDir, "test.hnb"), []byte("test"), 0644)

	// Create 5 snapshots with max=3.
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(modDir, "test.hnb"), []byte("iteration"), 0644)
		if _, err := SnapshotLive(liveDir, historyDir, 3); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}

	versions, _ := ListVersions(historyDir)
	if len(versions) > 3 {
		t.Errorf("pruning failed: %d versions, want <= 3", len(versions))
	}
}

func TestRollbackNonexistentVersion(t *testing.T) {
	historyDir := t.TempDir()
	liveDir := t.TempDir()

	err := RollbackToVersion(historyDir, "20260101T000000Z", liveDir)
	if err == nil {
		t.Error("expected error for nonexistent version")
	}
}
