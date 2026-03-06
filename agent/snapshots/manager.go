// Package snapshots manages file snapshots for rollback support.
package snapshots

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Manager handles saving, restoring, and pruning file snapshots.
type Manager struct {
	BaseDir    string
	MaxPerPath int
}

// pathHash returns the SHA256 hex digest of a file path, used as the snapshot
// filename prefix to group snapshots by source file.
func pathHash(filePath string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(filePath)))
}

// snapshotName returns the snapshot filename for a given file path and run ID.
func snapshotName(filePath, runID string) string {
	return pathHash(filePath) + "." + runID + ".bak"
}

// Save copies the current file at filePath to the snapshot directory, tagged
// with the given runID. The snapshot directory is created with mode 0700 if it
// does not exist. After saving, old snapshots are pruned to MaxPerPath.
func (m *Manager) Save(filePath, runID string) error {
	if err := os.MkdirAll(m.BaseDir, 0700); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	src, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()

	dstPath := filepath.Join(m.BaseDir, snapshotName(filePath, runID))
	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create snapshot file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy to snapshot: %w", err)
	}

	if err := dst.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
	}

	return m.Prune(filePath)
}

// Restore copies a snapshot back to the original file path.
func (m *Manager) Restore(filePath, snapshotPath string) error {
	src, err := os.Open(snapshotPath)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer src.Close()

	// Write to temp file in same dir, then rename for atomicity
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".isogov-restore-*")
	if err != nil {
		return fmt.Errorf("create temp for restore: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // clean up on failure
	}()

	if _, err := io.Copy(tmp, src); err != nil {
		return fmt.Errorf("copy snapshot to temp: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("rename restored file: %w", err)
	}

	return nil
}

// Prune removes old snapshots beyond MaxPerPath for the given file path,
// keeping only the most recent snapshots (sorted lexicographically by name,
// which preserves insertion order when run IDs are sequential).
func (m *Manager) Prune(filePath string) error {
	prefix := pathHash(filePath)
	entries, err := os.ReadDir(m.BaseDir)
	if err != nil {
		return fmt.Errorf("read snapshot dir: %w", err)
	}

	var snaps []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".bak") {
			snaps = append(snaps, e.Name())
		}
	}

	sort.Strings(snaps)

	if len(snaps) <= m.MaxPerPath {
		return nil
	}

	// Remove oldest snapshots (lowest sorted names)
	toRemove := snaps[:len(snaps)-m.MaxPerPath]
	for _, name := range toRemove {
		if err := os.Remove(filepath.Join(m.BaseDir, name)); err != nil {
			return fmt.Errorf("remove old snapshot %s: %w", name, err)
		}
	}

	return nil
}
