// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package publish

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultMaxVersions is the maximum number of config snapshots to keep.
const DefaultMaxVersions = 20

// snapshotVersionFormat names snapshots. Nanosecond resolution plus an
// O_EXCL create (with a counter on collision) keeps two snapshots taken in
// the same second from overwriting one archive. Fixed width, so the names
// sort chronologically as strings.
const snapshotVersionFormat = "20060102T150405.000000000Z"

// SnapshotTree writes a tar.gz snapshot of the publishable tree under
// srcDir into historyDir and returns its version name. Publish snapshots
// the staging tree it is about to sync (so a publish that cannot be
// snapshotted never reaches .live); PublishFile snapshots .live after its
// write. Hidden entries and root-level files atomicSync would not copy are
// left out, so the archive is exactly what .live holds. The archive is
// written to a temp name and renamed into place only after every writer
// has closed cleanly — a truncated archive never appears as a version.
// Prunes old snapshots beyond maxVersions.
func SnapshotTree(srcDir, historyDir string, maxVersions int) (string, error) {
	if maxVersions <= 0 {
		maxVersions = DefaultMaxVersions
	}
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		return "", fmt.Errorf("creating history dir: %w", err)
	}

	tmp, err := os.CreateTemp(historyDir, ".snapshot-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating archive: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpPath)
		}
	}()

	if err := writeTreeArchive(tmp, srcDir); err != nil {
		tmp.Close()
		return "", fmt.Errorf("creating archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing archive: %w", err)
	}

	base := time.Now().UTC().Format(snapshotVersionFormat)
	version := base
	for i := 1; ; i++ {
		final := filepath.Join(historyDir, version+".tar.gz")
		// Link (not rename) so an existing archive is never replaced.
		if err := os.Link(tmpPath, final); err == nil {
			break
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("committing archive: %w", err)
		}
		if i > 1000 {
			return "", fmt.Errorf("committing archive: too many snapshots named %s", base)
		}
		version = fmt.Sprintf("%s-%d", base, i)
	}
	os.Remove(tmpPath)
	committed = true

	pruneSnapshots(historyDir, maxVersions)
	return version, nil
}

// writeTreeArchive streams srcDir's publishable tree as tar.gz into w and
// reports every writer's Close error.
func writeTreeArchive(w io.Writer, srcDir string) error {
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)
	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == srcDir {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		// Skip hidden entries (.publish-tmp-*, .git, ...).
		if strings.HasPrefix(filepath.Base(path), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Root-level files atomicSync does not copy are not part of .live.
		if !info.IsDir() && !strings.Contains(rel, string(filepath.Separator)) && !isPublishableFile(info.Name()) {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil // symlinks, devices: never part of a config snapshot
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gw.Close()
}

// ListVersions returns available config snapshot versions, newest first.
func ListVersions(historyDir string) ([]string, error) {
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var versions []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		versions = append(versions, strings.TrimSuffix(e.Name(), ".tar.gz"))
	}

	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	return versions, nil
}

// RollbackToVersion extracts a snapshot tarball into the .live directory,
// replacing its contents.
//
// It holds the cross-process publish lock for the whole swap, so it can
// never rename .live away in the middle of a publish's sync, and it
// records the restored version as the live one.
func RollbackToVersion(historyDir, version, liveDir string) error {
	if version == "" || strings.ContainsAny(version, "/\\") || strings.HasPrefix(version, ".") {
		return fmt.Errorf("invalid snapshot version %q", version)
	}
	unlock, err := lockPublish(liveDir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := rollbackLocked(historyDir, version, liveDir); err != nil {
		return err
	}
	return recordLiveVersion(liveDir, version)
}

func rollbackLocked(historyDir, version, liveDir string) error {
	archivePath := filepath.Join(historyDir, version+".tar.gz")
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("snapshot %q not found", version)
	}

	// Create a temporary directory for extraction.
	tmpDir, err := os.MkdirTemp(filepath.Dir(liveDir), ".rollback-tmp-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(tmpDir, header.Name)

		// Zip-Slip containment: reject any entry whose cleaned target escapes
		// tmpDir (e.g. "../../etc/passwd" or an absolute path). filepath.Join
		// already cleans, so compare the cleaned target against the dir prefix.
		cleanRoot := filepath.Clean(tmpDir) + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanRoot) {
			return fmt.Errorf("rollback: refusing path traversal in archive entry %q", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, data, os.FileMode(header.Mode)); err != nil {
				return err
			}
		default:
			// Skip symlinks, hardlinks, devices, FIFOs, etc. — only plain
			// directories and regular files belong in a config snapshot, and a
			// symlink entry is another traversal/escape vector.
			continue
		}
	}

	// Atomic swap: backup existing live, rename tmp to live.
	backupDir := liveDir + ".rollback-backup"
	os.RemoveAll(backupDir) // clean up any prior failed rollback
	if _, err := os.Stat(liveDir); err == nil {
		if err := os.Rename(liveDir, backupDir); err != nil {
			return fmt.Errorf("backing up live: %w", err)
		}
	}
	if err := os.Rename(tmpDir, liveDir); err != nil {
		// Try to restore backup (best-effort; the swap failure is already being returned).
		_ = os.Rename(backupDir, liveDir)
		return fmt.Errorf("swapping live: %w", err)
	}
	os.RemoveAll(backupDir)

	return nil
}

func pruneSnapshots(historyDir string, maxVersions int) {
	versions, err := ListVersions(historyDir)
	if err != nil || len(versions) <= maxVersions {
		return
	}

	for _, v := range versions[maxVersions:] {
		os.Remove(filepath.Join(historyDir, v+".tar.gz"))
	}
}
