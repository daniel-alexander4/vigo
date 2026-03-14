// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

// SnapshotLive creates a tar.gz snapshot of the .live directory in the history
// directory, named by timestamp. Prunes old snapshots beyond maxVersions.
func SnapshotLive(liveDir, historyDir string, maxVersions int) (string, error) {
	if maxVersions <= 0 {
		maxVersions = DefaultMaxVersions
	}

	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		return "", fmt.Errorf("creating history dir: %w", err)
	}

	version := time.Now().UTC().Format("20060102T150405Z")
	archivePath := filepath.Join(historyDir, version+".tar.gz")

	f, err := os.Create(archivePath)
	if err != nil {
		return "", fmt.Errorf("creating archive: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = filepath.Walk(liveDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(liveDir, path)
		if err != nil {
			return err
		}
		// Skip hidden dirs (like .publish-tmp-*)
		if strings.HasPrefix(filepath.Base(path), ".") && path != liveDir {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

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
	if err != nil {
		os.Remove(archivePath)
		return "", fmt.Errorf("creating archive: %w", err)
	}

	// Prune old snapshots.
	pruneSnapshots(historyDir, maxVersions)

	return version, nil
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
func RollbackToVersion(historyDir, version, liveDir string) error {
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
		// Try to restore backup.
		os.Rename(backupDir, liveDir)
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
