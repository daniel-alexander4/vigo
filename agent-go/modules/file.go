// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hobnob/hobnob/agent/snapshots"
)

// FileExecutor manages file resources idempotently. It checks SHA256 content
// hashes before writing, uses atomic write-to-temp-then-rename, and optionally
// saves snapshots before overwriting existing files.
type FileExecutor struct {
	// SnapshotMgr is an optional snapshot manager. If non-nil, the executor
	// saves a snapshot of the existing file before overwriting it.
	SnapshotMgr *snapshots.Manager

	// RunID identifies the current convergence run, used for snapshot naming.
	RunID string
}

// Execute applies the desired file state based on params. Supported params:
//   - targetpath: target file path on the managed thrall (required)
//   - content: desired file content (required for state=present)
//   - baseindent: number of spaces to prepend to every non-empty line (default 0)
//   - mode: file permission mode as octal string (default "0644")
//   - owner: file owner (applied via chown)
//   - group: file group (applied via chown)
//   - state: "present" (default) or "absent"
func (f *FileExecutor) Execute(_ context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	result := ResourceResult{
		ResourceType: "file",
		ResourceName: params["targetpath"],
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	path := params["targetpath"]
	if path == "" {
		result.Error = fmt.Errorf("file executor: targetpath param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}

	if state == "absent" {
		return f.handleAbsent(path, dryRun, result)
	}

	return f.handlePresent(path, params, dryRun, result)
}

// handleAbsent removes a file if it exists.
func (f *FileExecutor) handleAbsent(path string, dryRun bool, result ResourceResult) ResourceResult {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		result.Action = ActionNone
		result.Changed = false
		return result
	}
	if err != nil {
		result.Error = fmt.Errorf("stat %s: %w", path, err)
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if err := os.Remove(path); err != nil {
		result.Error = fmt.Errorf("remove %s: %w", path, err)
	}
	return result
}

// handlePresent ensures a file exists with the desired content, mode, and ownership.
func (f *FileExecutor) handlePresent(path string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	content := params["content"]

	// Apply base indentation if specified
	if biStr := params["baseindent"]; biStr != "" {
		bi, err := strconv.Atoi(biStr)
		if err != nil {
			result.Error = fmt.Errorf("parse baseindent %q: %w", biStr, err)
			return result
		}
		if bi > 0 {
			content = applyBaseIndent(content, bi)
		}
	}

	modeStr := params["mode"]
	if modeStr == "" {
		modeStr = "0644"
	}

	mode, err := strconv.ParseUint(modeStr, 8, 32)
	if err != nil {
		result.Error = fmt.Errorf("parse mode %q: %w", modeStr, err)
		return result
	}
	fileMode := os.FileMode(mode)

	desiredHash := sha256Hex([]byte(content))

	// Check existing file
	existingInfo, statErr := os.Stat(path)
	fileExisted := statErr == nil

	// Resolve desired ownership.
	desiredUID, desiredGID := -1, -1
	if owner := params["owner"]; owner != "" {
		if u, err := user.Lookup(owner); err == nil {
			desiredUID, _ = strconv.Atoi(u.Uid)
		}
	}
	if group := params["group"]; group != "" {
		if g, err := user.LookupGroup(group); err == nil {
			desiredGID, _ = strconv.Atoi(g.Gid)
		}
	}

	if fileExisted {
		currentHash, hashErr := fileHash(path)
		if hashErr != nil {
			result.Error = fmt.Errorf("hash existing file %s: %w", path, hashErr)
			return result
		}

		ownershipMatch := true
		if stat, ok := existingInfo.Sys().(*syscall.Stat_t); ok {
			if desiredUID >= 0 && int(stat.Uid) != desiredUID {
				ownershipMatch = false
			}
			if desiredGID >= 0 && int(stat.Gid) != desiredGID {
				ownershipMatch = false
			}
		}

		if currentHash == desiredHash && existingInfo.Mode().Perm() == fileMode && ownershipMatch {
			result.Action = ActionNone
			result.Changed = false
			return result
		}

		// Only ownership differs — chown without rewriting the file.
		if currentHash == desiredHash && existingInfo.Mode().Perm() == fileMode && !ownershipMatch {
			result.Action = ActionUpdated
			result.Changed = true
			if dryRun {
				return result
			}
			if err := os.Chown(path, desiredUID, desiredGID); err != nil {
				result.Error = fmt.Errorf("chown %s: %w", path, err)
			}
			return result
		}

		// Content or mode differs
		result.Action = ActionUpdated
		result.Changed = true
	} else if os.IsNotExist(statErr) {
		result.Action = ActionCreated
		result.Changed = true
	} else {
		result.Error = fmt.Errorf("stat %s: %w", path, statErr)
		return result
	}

	if dryRun {
		return result
	}

	// Snapshot before overwrite (if manager is available and file existed)
	if f.SnapshotMgr != nil && fileExisted {
		if snapErr := f.SnapshotMgr.Save(path, f.RunID); snapErr != nil {
			result.Error = fmt.Errorf("snapshot save %s: %w", path, snapErr)
			return result
		}
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		result.Error = fmt.Errorf("create parent dir %s: %w", dir, err)
		return result
	}

	// Atomic write: create temp in same directory, write, chmod, rename.
	// Temp file MUST be in filepath.Dir(target) to avoid EXDEV on rename.
	tmp, err := os.CreateTemp(dir, ".hobnob-*")
	if err != nil {
		result.Error = fmt.Errorf("create temp file in %s: %w", dir, err)
		return result
	}
	tmpName := tmp.Name()
	defer func() {
		// Clean up temp file on any failure path
		os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		result.Error = fmt.Errorf("write temp file: %w", err)
		return result
	}

	if err := tmp.Close(); err != nil {
		result.Error = fmt.Errorf("close temp file: %w", err)
		return result
	}

	if err := os.Chmod(tmpName, fileMode); err != nil {
		result.Error = fmt.Errorf("chmod temp file: %w", err)
		return result
	}

	if err := os.Rename(tmpName, path); err != nil {
		result.Error = fmt.Errorf("rename temp to %s: %w", path, err)
		return result
	}

	// Apply ownership if specified.
	if desiredUID >= 0 || desiredGID >= 0 {
		if err := os.Chown(path, desiredUID, desiredGID); err != nil {
			result.Error = fmt.Errorf("chown %s: %w", path, err)
			return result
		}
	}

	return result
}

// sha256Hex returns the hex-encoded SHA256 hash of the given data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// applyBaseIndent prepends n spaces to every non-empty line of content.
func applyBaseIndent(content string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

// fileHash returns the hex-encoded SHA256 hash of a file's contents.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
