// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeFileDiff_NewFile(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "new.vgo")
	os.WriteFile(newPath, []byte("line1\nline2\n"), 0644)

	diff := ComputeFileDiff("", newPath)
	if !strings.Contains(diff, "+line1") {
		t.Errorf("new file diff should show added lines, got: %s", diff)
	}
}

func TestComputeFileDiff_DeletedFile(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.vgo")
	os.WriteFile(oldPath, []byte("line1\nline2\n"), 0644)

	diff := ComputeFileDiff(oldPath, "")
	if !strings.Contains(diff, "-line1") {
		t.Errorf("deleted file diff should show removed lines, got: %s", diff)
	}
}

func TestComputeFileDiff_ModifiedFile(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.vgo")
	newPath := filepath.Join(dir, "new.vgo")
	os.WriteFile(oldPath, []byte("line1\nline2\n"), 0644)
	os.WriteFile(newPath, []byte("line1\nline3\n"), 0644)

	diff := ComputeFileDiff(oldPath, newPath)
	if !strings.Contains(diff, "-line2") || !strings.Contains(diff, "+line3") {
		t.Errorf("modified file diff should show changed lines, got: %s", diff)
	}
}

func TestComputeFileDiff_IdenticalFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.vgo")
	os.WriteFile(path, []byte("identical\n"), 0644)

	diff := ComputeFileDiff(path, path)
	if diff != "" {
		t.Errorf("identical files should produce empty diff, got: %s", diff)
	}
}
