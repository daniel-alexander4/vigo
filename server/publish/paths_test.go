// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"path/filepath"
	"testing"
)

// TestPathsUnder pins the on-disk layout: the three publish dirs are siblings
// under the root, and the Default*Dir values are exactly PathsUnder of the
// default root. This guards the no-flag CLI path against drift — if the layout
// or a default ever changes, both the helper and the consumers move together.
func TestPathsUnder(t *testing.T) {
	// Defaults derive from PathsUnder(DefaultServerRoot).
	wantStacks := filepath.Join(DefaultServerRoot, "stacks")
	wantLive := filepath.Join(DefaultServerRoot, ".live")
	wantHistory := filepath.Join(DefaultServerRoot, "config-history")
	if DefaultStacksDir != wantStacks || DefaultLiveDir != wantLive || DefaultHistoryDir != wantHistory {
		t.Fatalf("Default*Dir drifted from PathsUnder(%q): got (%q, %q, %q), want (%q, %q, %q)",
			DefaultServerRoot, DefaultStacksDir, DefaultLiveDir, DefaultHistoryDir,
			wantStacks, wantLive, wantHistory)
	}

	// An arbitrary root anchors all three as siblings under it.
	const root = "/tmp/vigo-ephemeral"
	stacks, live, history := PathsUnder(root)
	if stacks != filepath.Join(root, "stacks") {
		t.Errorf("stacks = %q, want %q", stacks, filepath.Join(root, "stacks"))
	}
	if live != filepath.Join(root, ".live") {
		t.Errorf("live = %q, want %q", live, filepath.Join(root, ".live"))
	}
	if history != filepath.Join(root, "config-history") {
		t.Errorf("history = %q, want %q", history, filepath.Join(root, "config-history"))
	}
}
