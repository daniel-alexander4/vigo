// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTungolRole_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantRole TungolRole
	}{
		{name: "standalone", yaml: `role: standalone`, wantRole: RoleStandalone},
		{name: "hub", yaml: `role: hub`, wantRole: RoleHub},
		{name: "spoke", yaml: `role: spoke`, wantRole: RoleSpoke},
		{name: "empty defaults to empty string", yaml: `{}`, wantRole: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fs TungolSection
			if err := yaml.Unmarshal([]byte(tt.yaml), &fs); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if fs.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", fs.Role, tt.wantRole)
			}
		})
	}
}

func TestTungolSection_FullSteorraParse(t *testing.T) {
	input := `
tungol:
  role: spoke
  steorra_id: dc1
  hub_addr: hub.example.com:1530
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Tungol.Role != RoleSpoke {
		t.Errorf("role = %q, want spoke", cfg.Tungol.Role)
	}
	if cfg.Tungol.SteorraID != "dc1" {
		t.Errorf("steorra_id = %q, want dc1", cfg.Tungol.SteorraID)
	}
	if cfg.Tungol.HubAddr != "hub.example.com:1530" {
		t.Errorf("hub_addr = %q, want hub.example.com:1530", cfg.Tungol.HubAddr)
	}
}

func TestTungolSection_FullHubParse(t *testing.T) {
	input := `
tungol:
  role: hub
  steorras:
    - id: dc1
      addr: steorra1.example.com:1530
    - id: dc2
      addr: steorra2.example.com:1530
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Tungol.Role != RoleHub {
		t.Errorf("role = %q, want hub", cfg.Tungol.Role)
	}
	if len(cfg.Tungol.Steorras) != 2 {
		t.Fatalf("steorras len = %d, want 2", len(cfg.Tungol.Steorras))
	}
	if cfg.Tungol.Steorras[0].ID != "dc1" {
		t.Errorf("steorras[0].id = %q, want dc1", cfg.Tungol.Steorras[0].ID)
	}
	if cfg.Tungol.Steorras[1].Addr != "steorra2.example.com:1530" {
		t.Errorf("steorras[1].addr = %q, want steorra2.example.com:1530", cfg.Tungol.Steorras[1].Addr)
	}
}

func TestTungolSection_Validate(t *testing.T) {
	tests := []struct {
		name    string
		section TungolSection
		wantErr bool
	}{
		{name: "empty is valid", section: TungolSection{}},
		{name: "standalone", section: TungolSection{Role: RoleStandalone}},
		{
			name: "hub with steorras",
			section: TungolSection{
				Role:     RoleHub,
				Steorras: []SteorraEntry{{ID: "dc1", Addr: "steorra1:1530"}},
			},
		},
		{name: "hub without steorras", section: TungolSection{Role: RoleHub}, wantErr: true},
		{
			name: "hub with steorra missing id",
			section: TungolSection{
				Role:     RoleHub,
				Steorras: []SteorraEntry{{Addr: "steorra1:1530"}},
			},
			wantErr: true,
		},
		{
			name: "hub with steorra missing addr",
			section: TungolSection{
				Role:     RoleHub,
				Steorras: []SteorraEntry{{ID: "dc1"}},
			},
			wantErr: true,
		},
		{name: "spoke with id", section: TungolSection{Role: RoleSpoke, SteorraID: "dc1"}},
		{name: "spoke without id", section: TungolSection{Role: RoleSpoke}, wantErr: true},
		{name: "invalid role", section: TungolSection{Role: "bogus"}, wantErr: true},
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

	if cfg.Tungol.Role != "" {
		t.Errorf("role should be empty when omitted, got %q", cfg.Tungol.Role)
	}
	if cfg.Tungol.SteorraID != "" {
		t.Errorf("steorra_id should be empty when omitted, got %q", cfg.Tungol.SteorraID)
	}
	if len(cfg.Tungol.Steorras) != 0 {
		t.Errorf("steorras should be empty when omitted, got %d", len(cfg.Tungol.Steorras))
	}
}
