// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package license

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const graceStartFile = "grace_start"

// readGraceStart reads the grace period start timestamp from disk.
// Format: RFC3339 timestamp followed by a newline and the violation reason.
func readGraceStart(dir string) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(dir, graceStartFile))
	if err != nil {
		return time.Time{}, err
	}

	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) == 0 {
		return time.Time{}, fmt.Errorf("empty grace_start file")
	}

	return time.Parse(time.RFC3339, lines[0])
}

// writeGraceStart persists the grace period start timestamp to disk.
// Refuses to overwrite an existing file to prevent clock resets.
func writeGraceStart(dir string, t time.Time) {
	path := filepath.Join(dir, graceStartFile)

	// Refuse to overwrite — guards against clock reset attempts.
	if _, err := os.Stat(path); err == nil {
		return
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	content := t.Format(time.RFC3339)
	_ = os.WriteFile(path, []byte(content), 0644)
}

// clearGraceStart removes the grace start file when returning to compliant.
func clearGraceStart(dir string) {
	path := filepath.Join(dir, graceStartFile)
	_ = os.Remove(path)
}
