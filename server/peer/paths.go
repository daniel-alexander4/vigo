// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package peer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// replicatedDirs are the top-level directories under serverRoot that HA
// replication carries, on BOTH sides: the primary watches and full-syncs
// exactly these, and a peer accepts a write or delete only under one of them.
//
//   - stacks, .live: the operator stacks tree and the validated tree the server
//     reads — the layout publish.PathsUnder owns (layout_test.go pins the two
//     names against it; peer cannot import publish without an import cycle).
//     config-history is deliberately NOT replicated: it is a local rollback
//     archive, and a failed-over primary rolls back from its own history.
//   - secrets, tls: the local secrets backend's files and the server TLS
//     material.
//
// Everything else under serverRoot — notably roleStateFile (peer-role), which
// must never be replicated — is refused on receive.
var replicatedDirs = []string{"stacks", ".live", "secrets", "tls"}

// replicatedFile is the single top-level file replicated alongside
// replicatedDirs.
const replicatedFile = "server.yaml"

// ReplicatedDirs returns a copy of the replicated top-level directory names.
func ReplicatedDirs() []string { return append([]string(nil), replicatedDirs...) }

// replicatedTop returns the top-level replicated entry rel falls under
// (a replicatedDirs name, or replicatedFile), or "" when rel is outside the
// replicated set. rel is relative to serverRoot.
func replicatedTop(rel string) string {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == replicatedFile {
		return replicatedFile
	}
	for _, dir := range replicatedDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return dir
		}
	}
	return ""
}

// isConfigPath reports whether rel is under the config tree, so a write there
// triggers a config reload on the peer.
func isConfigPath(rel string) bool {
	top := replicatedTop(rel)
	return top == "stacks" || top == ".live"
}

// containedTarget returns where a replicated write/delete of rel lands, and
// refuses it unless the deepest existing ancestor of the target, after symlink
// resolution, is still inside the resolved top-level replicated entry. The
// top-level entry itself is the local operator's layout and may be a symlink
// (e.g. tls → /etc/vigo/tls); a symlink planted BELOW it that points elsewhere
// is refused. The final path component is never followed — a write renames
// over it and a delete unlinks it.
func containedTarget(serverRoot, rel string) (string, error) {
	top := replicatedTop(rel)
	if top == "" {
		return "", fmt.Errorf("path not in the replicated set: %s", rel)
	}
	full := filepath.Join(serverRoot, rel)
	if top == replicatedFile || filepath.Clean(rel) == top {
		if top != replicatedFile {
			return "", fmt.Errorf("refusing to replace replicated directory root: %s", rel)
		}
		return full, nil // direct child of serverRoot; nothing below to follow
	}
	topAbs := filepath.Join(serverRoot, top)
	topReal, err := filepath.EvalSymlinks(topAbs)
	if err != nil {
		// Top-level dir not created yet: nothing under it can be a symlink.
		return full, nil
	}
	// Deepest existing ancestor of the target's parent.
	anc := filepath.Dir(full)
	for anc != topAbs {
		if _, statErr := os.Lstat(anc); statErr == nil {
			break
		}
		anc = filepath.Dir(anc)
	}
	ancReal, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rel, err)
	}
	if ancReal != topReal && !strings.HasPrefix(ancReal, topReal+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes %s/ via a symlink: %s", top, rel)
	}
	return full, nil
}
