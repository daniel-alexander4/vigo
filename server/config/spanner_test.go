// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSpannerMode_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantMode SpannerMode
	}{
		{name: "standalone", yaml: `mode: standalone`, wantMode: ModeStandalone},
		{name: "founder", yaml: `mode: founder`, wantMode: ModeFounder},
		{name: "joiner", yaml: `mode: joiner`, wantMode: ModeJoiner},
		{name: "empty defaults to empty string", yaml: `{}`, wantMode: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fs SpannerSection
			if err := yaml.Unmarshal([]byte(tt.yaml), &fs); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if fs.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", fs.Mode, tt.wantMode)
			}
		})
	}
}

func TestSpannerSection_FullJoinerParse(t *testing.T) {
	input := `
spanner:
  mode: joiner
  spanner_id: alexander4-prod
  patterns: ["*.dc1.*"]
  transport:
    bootstrap_peers: ["founder.example.com:1530"]
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Spanner.Mode != ModeJoiner {
		t.Errorf("mode = %q, want joiner", cfg.Spanner.Mode)
	}
	if cfg.Spanner.SpannerID != "alexander4-prod" {
		t.Errorf("spanner_id = %q, want alexander4-prod", cfg.Spanner.SpannerID)
	}
	if got, want := cfg.Spanner.Patterns, []string{"*.dc1.*"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("patterns = %v, want %v", got, want)
	}
	if got := cfg.Spanner.Transport.BootstrapPeers; len(got) != 1 || got[0] != "founder.example.com:1530" {
		t.Errorf("bootstrap_peers = %v, want [founder.example.com:1530]", got)
	}
}

func TestSpannerSection_FullFounderParse(t *testing.T) {
	input := `
spanner:
  mode: founder
  spanner_id: alexander4-prod
  patterns: ["*.us-west.*", "*.us-east.*"]
  snapshot:
    tier1_interval: 30s
    tier2_interval: 5m
  transport:
    multicast_group: 224.0.0.45
    multicast_port: 1534
  fallback_admin: local
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Spanner.Mode != ModeFounder {
		t.Errorf("mode = %q, want founder", cfg.Spanner.Mode)
	}
	if len(cfg.Spanner.Patterns) != 2 {
		t.Fatalf("patterns len = %d, want 2", len(cfg.Spanner.Patterns))
	}
	if cfg.Spanner.Snapshot.Tier1Interval != "30s" {
		t.Errorf("tier1_interval = %q, want 30s", cfg.Spanner.Snapshot.Tier1Interval)
	}
	if cfg.Spanner.Transport.MulticastGroup != "224.0.0.45" {
		t.Errorf("multicast_group = %q, want 224.0.0.45", cfg.Spanner.Transport.MulticastGroup)
	}
	if cfg.Spanner.FallbackAdmin != "local" {
		t.Errorf("fallback_admin = %q, want local", cfg.Spanner.FallbackAdmin)
	}
}

func TestSpannerSection_Validate(t *testing.T) {
	founderSection := func() SpannerSection {
		return SpannerSection{
			Mode:      ModeFounder,
			SpannerID: "alexander4-prod",
			Patterns:  []string{"*.us-west.*"},
		}
	}
	joinerSection := func() SpannerSection {
		return SpannerSection{
			Mode:      ModeJoiner,
			SpannerID: "alexander4-prod",
			Patterns:  []string{"*.dc1.*"},
			Transport: TransportConfig{BootstrapPeers: []string{"founder:1530"}},
		}
	}

	tests := []struct {
		name    string
		section SpannerSection
		wantErr bool
	}{
		{name: "empty is valid", section: SpannerSection{}},
		{name: "standalone", section: SpannerSection{Mode: ModeStandalone}},
		{name: "founder full", section: founderSection()},
		{
			name: "founder without spanner_id",
			section: SpannerSection{
				Mode:     ModeFounder,
				Patterns: []string{"*.us-west.*"},
			},
			wantErr: true,
		},
		{
			name: "founder with invalid spanner_id (has space)",
			section: SpannerSection{
				Mode:      ModeFounder,
				SpannerID: "bad name",
				Patterns:  []string{"*.us-west.*"},
			},
			wantErr: true,
		},
		{
			name: "founder without patterns",
			section: SpannerSection{
				Mode:      ModeFounder,
				SpannerID: "alexander4-prod",
			},
			wantErr: true,
		},
		{name: "joiner full", section: joinerSection()},
		{
			name: "joiner without bootstrap_peers",
			section: SpannerSection{
				Mode:      ModeJoiner,
				SpannerID: "alexander4-prod",
				Patterns:  []string{"*.dc1.*"},
			},
			wantErr: true,
		},
		{
			name: "joiner without patterns",
			section: SpannerSection{
				Mode:      ModeJoiner,
				SpannerID: "alexander4-prod",
				Transport: TransportConfig{BootstrapPeers: []string{"founder:1530"}},
			},
			wantErr: true,
		},
		{
			name:    "invalid mode",
			section: SpannerSection{Mode: "bogus", SpannerID: "alexander4-prod"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.section.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpannerSection_OmittedDefaults(t *testing.T) {
	input := `
server:
  grpc_listen: ":1530"
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Spanner.Mode != "" {
		t.Errorf("mode should be empty when omitted, got %q", cfg.Spanner.Mode)
	}
	if len(cfg.Spanner.Patterns) != 0 {
		t.Errorf("patterns should be empty when omitted, got %d", len(cfg.Spanner.Patterns))
	}
	if len(cfg.Spanner.Transport.BootstrapPeers) != 0 {
		t.Errorf("bootstrap_peers should be empty when omitted, got %d", len(cfg.Spanner.Transport.BootstrapPeers))
	}
}

// Loader hard-errors on retired pre-Phase-2 keys per ADR-026.
func TestDetectRetiredSpannerKeys(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		wantSub string // substring expected in the error message
	}{
		{name: "clean (no spanner section)", yaml: `server:\n  grpc_listen: :1530\n`},
		{name: "clean (new shape only)", yaml: "spanner:\n  mode: founder\n"},
		{
			name:    "retired role",
			yaml:    "spanner:\n  role: hub\n",
			wantErr: true,
			wantSub: "spanner.role",
		},
		{
			name:    "retired bolts",
			yaml:    "spanner:\n  bolts:\n    - id: dc1\n      addr: x:1\n",
			wantErr: true,
			wantSub: "spanner.bolts",
		},
		{
			name:    "retired hub_addr",
			yaml:    "spanner:\n  hub_addr: hub.example.com:1530\n",
			wantErr: true,
			wantSub: "spanner.hub_addr",
		},
		{
			name:    "retired hub_fallback_addrs",
			yaml:    "spanner:\n  hub_fallback_addrs: [hub2:1530]\n",
			wantErr: true,
			wantSub: "spanner.hub_fallback_addrs",
		},
		{
			name:    "retired auto_failover",
			yaml:    "spanner:\n  auto_failover:\n    enabled: true\n",
			wantErr: true,
			wantSub: "spanner.auto_failover",
		},
		{
			name:    "retired bolt_id",
			yaml:    "spanner:\n  bolt_id: dc1\n",
			wantErr: true,
			wantSub: "spanner.bolt_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := detectRetiredSpannerKeys([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("detectRetiredSpannerKeys err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.wantSub != "" {
				if got := err.Error(); !contains(got, tt.wantSub) {
					t.Errorf("error %q does not mention %q", got, tt.wantSub)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
