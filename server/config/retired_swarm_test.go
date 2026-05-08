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
			name: "0.41.0 shape passes",
			yaml: `
swarm:
  enabled: ["*"]
  puddle: { enabled: ["annlap"] }
  gitmirror: { enabled: ["*"] }
  filesync: { enabled: ["*"] }
  payload: { enabled: ["*"] }
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
			name: "swarm.filesync.blacklist is retired",
			yaml: `
swarm:
  filesync:
    blacklist: ["rasp1"]
`,
			wantErr:     true,
			wantInError: "swarm.filesync.blacklist",
		},
		{
			name: "swarm.payload.blacklist is retired",
			yaml: `
swarm:
  payload:
    blacklist: ["rasp1"]
`,
			wantErr:     true,
			wantInError: "swarm.payload.blacklist",
		},
		{
			name: "per-subsystem bool enabled is retired",
			yaml: `
swarm:
  payload:
    enabled: false
`,
			wantErr:     true,
			wantInError: "swarm.payload.enabled is a bool",
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
