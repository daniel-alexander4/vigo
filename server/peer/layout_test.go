// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package peer_test

import (
	"path/filepath"
	"slices"
	"testing"

	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/publish"
)

// TestReplicatedDirsMatchPublishLayout: the config dirs HA replication carries
// are the ones publish.PathsUnder lays out. peer cannot import publish (cycle
// via fleet → ui → peer), so this external test is the tie.
func TestReplicatedDirsMatchPublishLayout(t *testing.T) {
	root := "/srv/vigo"
	stacks, live, _ := publish.PathsUnder(root)
	dirs := peer.ReplicatedDirs()
	for _, abs := range []string{stacks, live} {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(dirs, rel) {
			t.Errorf("publish dir %q is not replicated (ReplicatedDirs = %v)", rel, dirs)
		}
	}
}
