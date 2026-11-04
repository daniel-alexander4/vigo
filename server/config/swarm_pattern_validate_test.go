// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMalformedDenyPatternFailsClosed: a malformed deny glob ahead of a
// catch-all denies the host instead of being skipped (which let `*` allow
// the host the operator meant to deny).
func TestMalformedDenyPatternFailsClosed(t *testing.T) {
	if HostMatchesPatterns("rasp1", []string{"-[rasp1", "*"}) {
		t.Fatal("malformed deny pattern was skipped and the catch-all allowed the host")
	}
	if !HostMatchesPatterns("plex", []string{"[bad", "*"}) {
		t.Fatal("a malformed allow pattern should be skipped, letting the next pattern decide")
	}
}

// TestLoadServerConfigRejectsMalformedSwarmGlob: LoadServerConfig refuses
// a server.yaml with a malformed swarm pattern and names it.
func TestLoadServerConfigRejectsMalformedSwarmGlob(t *testing.T) {
	for _, pat := range []string{"-[rasp1", "[web"} {
		path := filepath.Join(t.TempDir(), "server.yaml")
		body := "swarm:\n  enabled: [\"*\"]\n  gitback:\n    enabled: [\"" + pat + "\", \"*\"]\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadServerConfig(path, &mockProvider{secrets: map[string]string{}}, "local")
		if err == nil || !strings.Contains(err.Error(), "swarm.gitback.enabled[0]") {
			t.Errorf("pattern %q: LoadServerConfig err = %v, want a swarm.gitback.enabled[0] glob error", pat, err)
		}
	}
}
