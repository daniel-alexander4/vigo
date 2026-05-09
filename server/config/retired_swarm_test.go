// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"strings"
	"testing"
)

func TestDetectRetiredSwarmShape(t *testing.T) {
	cases := []struct {
		name        string
		yaml        string
		wantErr     bool
		wantInError string // substring expected in error message
	}{
		{
			name: "current shape passes",
			yaml: `
swarm:
  enabled: ["*"]
  puddle: { enabled: ["annlap"] }
  gitmirror: { enabled: ["*"] }
  longdrawer: { enabled: ["*"] }
  swarmcast: { enabled: ["*"] }
`,
			wantErr: false,
		},
		{
			name:        "swarm.enabled as bool is retired",
			yaml:        "swarm:\n  enabled: true\n",
			wantErr:     true,
			wantInError: "swarm.enabled is a bool",
		},
		{
			name: "swarm.cryptsync section is retired",
			yaml: `
swarm:
  enabled: ["*"]
  cryptsync:
    enabled: true
`,
			wantErr:     true,
			wantInError: "swarm.cryptsync",
		},
		{
			name: "swarm.gitmirror.blacklist is retired",
			yaml: `
swarm:
  gitmirror:
    blacklist: ["rasp1"]
`,
			wantErr:     true,
			wantInError: "swarm.gitmirror.blacklist",
		},
		{
			name: "swarm.filesync section is retired (renamed to longdrawer)",
			yaml: `
swarm:
  filesync:
    enabled: ["*"]
`,
			wantErr:     true,
			wantInError: "swarm.filesync.* — retired in the longdrawer rename",
		},
		{
			name: "swarm.filesync retired error names the new key",
			yaml: `
swarm:
  filesync:
    enabled: ["*"]
`,
			wantErr:     true,
			wantInError: "swarm.longdrawer",
		},
		{
			name: "swarm.payload section is retired (renamed to swarmcast)",
			yaml: `
swarm:
  payload:
    enabled: ["*"]
`,
			wantErr:     true,
			wantInError: "swarm.payload.* — retired in the swarmcast rename",
		},
		{
			name: "swarm.payload retired error names the new key",
			yaml: `
swarm:
  payload:
    enabled: ["*"]
`,
			wantErr:     true,
			wantInError: "swarm.swarmcast",
		},
		{
			name: "per-subsystem bool enabled is retired (gitmirror)",
			yaml: `
swarm:
  gitmirror:
    enabled: false
`,
			wantErr:     true,
			wantInError: "swarm.gitmirror.enabled is a bool",
		},
		{
			name: "migration pointer included",
			yaml: `
swarm:
  enabled: true
`,
			wantErr:     true,
			wantInError: "0.41.0-migration",
		},
		{
			name: "multiple retired fields collected in one error",
			yaml: `
swarm:
  enabled: true
  cryptsync:
    enabled: true
  gitmirror:
    blacklist: ["rasp1"]
`,
			wantErr:     true,
			wantInError: "swarm.cryptsync",
		},
		{
			name:    "no swarm section is fine",
			yaml:    "auth:\n  type: basic\n",
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := detectRetiredSwarmShape([]byte(tc.yaml))
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if tc.wantInError != "" && err != nil {
				if !strings.Contains(err.Error(), tc.wantInError) {
					t.Errorf("error %q missing substring %q", err.Error(), tc.wantInError)
				}
			}
		})
	}
}
