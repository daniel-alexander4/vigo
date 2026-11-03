// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ComputeFileDiff returns a simple unified-style diff between two files.
// Returns empty string if files are identical or both don't exist.
func ComputeFileDiff(oldPath, newPath string) string {
	oldData, oldErr := os.ReadFile(oldPath)
	newData, newErr := os.ReadFile(newPath)

	if oldErr != nil && newErr != nil {
		return ""
	}

	oldLines := splitLines(string(oldData))
	newLines := splitLines(string(newData))

	if oldErr != nil {
		// New file.
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("--- /dev/null\n+++ %s\n", newPath))
		for _, line := range newLines {
			sb.WriteString("+" + line + "\n")
		}
		return sb.String()
	}

	if newErr != nil {
		// Deleted file.
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("--- %s\n+++ /dev/null\n", oldPath))
		for _, line := range oldLines {
			sb.WriteString("-" + line + "\n")
		}
		return sb.String()
	}

	// Simple line-by-line diff (not a full LCS algorithm, but sufficient for config files).
	return simpleDiff(oldPath, newPath, oldLines, newLines)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// Remove trailing empty line from final newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func simpleDiff(oldPath, newPath string, oldLines, newLines []string) string {
	if strings.Join(oldLines, "\n") == strings.Join(newLines, "\n") {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- %s\n+++ %s\n", oldPath, newPath))

	// Build a set of old lines for quick lookup.
	oldSet := make(map[string]bool, len(oldLines))
	for _, l := range oldLines {
		oldSet[l] = true
	}
	newSet := make(map[string]bool, len(newLines))
	for _, l := range newLines {
		newSet[l] = true
	}

	// Show removed lines.
	for _, l := range oldLines {
		if !newSet[l] {
			sb.WriteString("-" + l + "\n")
		}
	}
	// Show added lines.
	for _, l := range newLines {
		if !oldSet[l] {
			sb.WriteString("+" + l + "\n")
		}
	}

	return sb.String()
}

// ComputeAllDiffs returns a combined diff summary for all changed and removed files.
func ComputeAllDiffs(stacksDir, liveDir string, changed, removed []string) string {
	var sb strings.Builder

	for _, rel := range changed {
		oldPath := filepath.Join(liveDir, rel)
		newPath := filepath.Join(stacksDir, rel)
		diff := ComputeFileDiff(oldPath, newPath)
		if diff != "" {
			sb.WriteString(diff)
			sb.WriteString("\n")
		}
	}

	for _, rel := range removed {
		oldPath := filepath.Join(liveDir, rel)
		diff := ComputeFileDiff(oldPath, "")
		if diff != "" {
			sb.WriteString(diff)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
