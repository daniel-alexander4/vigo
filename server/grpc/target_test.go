// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"

	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm"
)

func TestMatchTarget(t *testing.T) {
	web01 := fleet.EnvoyState{Hostname: "web-01.example.com", Traits: map[string]string{"os.distro": "ubuntu"}}
	rasp01 := fleet.EnvoyState{Hostname: "rasp-01", Traits: map[string]string{"os.distro": "raspbian"}}
	danlap := fleet.EnvoyState{Hostname: "danlap", Traits: map[string]string{"os.distro": "ubuntu"}}

	tests := []struct {
		name  string
		envoy fleet.EnvoyState
		spec  string
		want  bool
	}{
		{"wildcard matches all", web01, "*", true},
		{"exact hostname match", web01, "web-01.example.com", true},
		{"exact hostname no match", web01, "web-02.example.com", false},
		{"glob match", web01, "web-*", true},
		{"glob no match", rasp01, "web-*", false},
		{"trait match", web01, "os.distro=ubuntu", true},
		{"trait no match", rasp01, "os.distro=ubuntu", false},

		// Exclusion with - prefix
		{"exclude match rejects", rasp01, "-rasp*", false},
		{"exclude no match includes", web01, "-rasp*", true},
		{"exclude-only defaults to all", danlap, "-rasp*", true},

		// Comma-separated
		{"include and exclude", web01, "web-*,-rasp*", true},
		{"include and exclude rejects excluded", rasp01, "web-*,-rasp*", false},
		{"multiple includes", web01, "web-*,rasp-*", true},
		{"multiple includes second match", rasp01, "web-*,rasp-*", true},
		{"multiple excludes", danlap, "-web-*,-rasp*", true},
		{"multiple excludes rejects first", web01, "-web-*,-rasp*", false},
		{"multiple excludes rejects second", rasp01, "-web-*,-rasp*", false},

		// Whitespace handling
		{"spaces around comma", web01, "web-* , -rasp*", true},
		{"empty parts ignored", web01, "web-*,,,-rasp*", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := swarm.MatchTarget(tt.envoy.Hostname, tt.envoy.Traits, tt.spec)
			if got != tt.want {
				t.Errorf("matchTarget(%q, %q) = %v, want %v", tt.envoy.Hostname, tt.spec, got, tt.want)
			}
		})
	}
}
