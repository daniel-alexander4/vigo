// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTungolMode_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantMode TungolMode
	}{
		{name: "standalone", yaml: `mode: standalone`, wantMode: ModeStandalone},
		{name: "founder", yaml: `mode: founder`, wantMode: ModeFounder},
		{name: "joiner", yaml: `mode: joiner`, wantMode: ModeJoiner},
		{name: "empty defaults to empty string", yaml: `{}`, wantMode: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fs TungolSection
			if err := yaml.Unmarshal([]byte(tt.yaml), &fs); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if fs.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", fs.Mode, tt.wantMode)
			}
		})
	}
}

func TestTungolSection_FullJoinerParse(t *testing.T) {
	input := `
tungol:
  mode: joiner
  tungol_id: alexander4-prod
  patterns: ["*.dc1.*"]
  transport:
    bootstrap_peers: ["founder.example.com:1530"]
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Tungol.Mode != ModeJoiner {
		t.Errorf("mode = %q, want joiner", cfg.Tungol.Mode)
	}
	if cfg.Tungol.TungolID != "alexander4-prod" {
		t.Errorf("tungol_id = %q, want alexander4-prod", cfg.Tungol.TungolID)
	}
	if got, want := cfg.Tungol.Patterns, []string{"*.dc1.*"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("patterns = %v, want %v", got, want)
	}
	if got := cfg.Tungol.Transport.BootstrapPeers; len(got) != 1 || got[0] != "founder.example.com:1530" {
		t.Errorf("bootstrap_peers = %v, want [founder.example.com:1530]", got)
	}
}

func TestTungolSection_FullFounderParse(t *testing.T) {
	input := `
tungol:
  mode: founder
  tungol_id: alexander4-prod
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

	if cfg.Tungol.Mode != ModeFounder {
		t.Errorf("mode = %q, want founder", cfg.Tungol.Mode)
	}
	if len(cfg.Tungol.Patterns) != 2 {
		t.Fatalf("patterns len = %d, want 2", len(cfg.Tungol.Patterns))
	}
	if cfg.Tungol.Snapshot.Tier1Interval != "30s" {
		t.Errorf("tier1_interval = %q, want 30s", cfg.Tungol.Snapshot.Tier1Interval)
	}
	if cfg.Tungol.Transport.MulticastGroup != "224.0.0.45" {
		t.Errorf("multicast_group = %q, want 224.0.0.45", cfg.Tungol.Transport.MulticastGroup)
	}
	if cfg.Tungol.FallbackAdmin != "local" {
		t.Errorf("fallback_admin = %q, want local", cfg.Tungol.FallbackAdmin)
	}
}

func TestTungolSection_Validate(t *testing.T) {
	founderSection := func() TungolSection {
		return TungolSection{
			Mode:     ModeFounder,
			TungolID: "alexander4-prod",
			Patterns: []string{"*.us-west.*"},
		}
	}
	joinerSection := func() TungolSection {
		return TungolSection{
			Mode:      ModeJoiner,
			TungolID:  "alexander4-prod",
			Patterns:  []string{"*.dc1.*"},
			Transport: TransportConfig{BootstrapPeers: []string{"founder:1530"}},
		}
	}

	tests := []struct {
		name    string
		section TungolSection
		wantErr bool
	}{
		{name: "empty is valid", section: TungolSection{}},
		{name: "standalone", section: TungolSection{Mode: ModeStandalone}},
		{name: "founder full", section: founderSection()},
		{
			name: "founder without tungol_id",
			section: TungolSection{
				Mode:     ModeFounder,
				Patterns: []string{"*.us-west.*"},
			},
			wantErr: true,
		},
		{
			name: "founder with invalid tungol_id (has space)",
			section: TungolSection{
				Mode:     ModeFounder,
				TungolID: "bad name",
				Patterns: []string{"*.us-west.*"},
			},
			wantErr: true,
		},
		{
			name: "founder without patterns",
			section: TungolSection{
				Mode:     ModeFounder,
				TungolID: "alexander4-prod",
			},
			wantErr: true,
		},
		{name: "joiner full", section: joinerSection()},
		{
			name: "joiner without bootstrap_peers",
			section: TungolSection{
				Mode:     ModeJoiner,
				TungolID: "alexander4-prod",
				Patterns: []string{"*.dc1.*"},
			},
			wantErr: true,
		},
		{
			name: "joiner without patterns",
			section: TungolSection{
				Mode:      ModeJoiner,
				TungolID:  "alexander4-prod",
				Transport: TransportConfig{BootstrapPeers: []string{"founder:1530"}},
			},
			wantErr: true,
		},
		{
			name:    "invalid mode",
			section: TungolSection{Mode: "bogus", TungolID: "alexander4-prod"},
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

func TestTungolSection_OmittedDefaults(t *testing.T) {
	input := `
server:
  grpc_listen: ":1530"
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Tungol.Mode != "" {
		t.Errorf("mode should be empty when omitted, got %q", cfg.Tungol.Mode)
	}
	if len(cfg.Tungol.Patterns) != 0 {
		t.Errorf("patterns should be empty when omitted, got %d", len(cfg.Tungol.Patterns))
	}
	if len(cfg.Tungol.Transport.BootstrapPeers) != 0 {
		t.Errorf("bootstrap_peers should be empty when omitted, got %d", len(cfg.Tungol.Transport.BootstrapPeers))
	}
}

// Loader hard-errors on retired pre-Phase-2 keys per ADR-026.
func TestDetectRetiredTungolKeys(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		wantSub string // substring expected in the error message
	}{
		{name: "clean (no tungol section)", yaml: `server:\n  grpc_listen: :1530\n`},
		{name: "clean (new shape only)", yaml: "tungol:\n  mode: founder\n"},
		{
			name:    "retired role",
			yaml:    "tungol:\n  role: hub\n",
			wantErr: true,
			wantSub: "tungol.role",
		},
		{
			name:    "retired steorras",
			yaml:    "tungol:\n  steorras:\n    - id: dc1\n      addr: x:1\n",
			wantErr: true,
			wantSub: "tungol.steorras",
		},
		{
			name:    "retired hub_addr",
			yaml:    "tungol:\n  hub_addr: hub.example.com:1530\n",
			wantErr: true,
			wantSub: "tungol.hub_addr",
		},
		{
			name:    "retired hub_fallback_addrs",
			yaml:    "tungol:\n  hub_fallback_addrs: [hub2:1530]\n",
			wantErr: true,
			wantSub: "tungol.hub_fallback_addrs",
		},
		{
			name:    "retired auto_failover",
			yaml:    "tungol:\n  auto_failover:\n    enabled: true\n",
			wantErr: true,
			wantSub: "tungol.auto_failover",
		},
		{
			name:    "retired steorra_id",
			yaml:    "tungol:\n  steorra_id: dc1\n",
			wantErr: true,
			wantSub: "tungol.steorra_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := detectRetiredTungolKeys([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("detectRetiredTungolKeys err = %v, wantErr %v", err, tt.wantErr)
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
