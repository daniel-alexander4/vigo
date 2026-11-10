// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResetEnvoyScriptNeverTouchesServerState: scripts/reset-envoy.sh is
// what the troubleshooting guide runs to reset an envoy, including on a
// server host that runs its own envoy. It once ended with
// `find /srv/vigo -mindepth 1 -delete`, wiping the master key, CA, DB and
// server.yaml. No command line in it may name the server root.
func TestResetEnvoyScriptNeverTouchesServerState(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "reset-envoy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "echo ") {
			continue
		}
		if strings.Contains(s, "/srv") {
			t.Errorf("scripts/reset-envoy.sh:%d touches server state: %s", i+1, s)
		}
	}
}
