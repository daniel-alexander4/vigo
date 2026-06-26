// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

import (
	"fmt"
	"log/slog"
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
// Refuses to overwrite an existing file to prevent clock resets. Returns
// an error if the timestamp could not be persisted — the caller must
// surface it, because an unpersisted grace start resets to zero on the
// next restart, indefinitely extending the staged-enforcement runway and
// defeating Service-Stop. Perms match the rest of the license dir
// (0700/0600), not the world-readable default.
func writeGraceStart(dir string, t time.Time) error {
	path := filepath.Join(dir, graceStartFile)

	// Refuse to overwrite — guards against clock reset attempts.
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create grace dir %s: %w", dir, err)
	}

	content := t.Format(time.RFC3339)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("write grace start file %s: %w", path, err)
	}
	return nil
}

// clearGraceStart removes the grace start file when returning to compliant.
func clearGraceStart(dir string) {
	path := filepath.Join(dir, graceStartFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Error("failed to remove grace start file", "path", path, "error", err)
	}
}
