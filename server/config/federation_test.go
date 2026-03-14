// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFederationRole_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantRole FederationRole
	}{
		{name: "standalone", yaml: `role: standalone`, wantRole: RoleStandalone},
		{name: "hub", yaml: `role: hub`, wantRole: RoleHub},
		{name: "spoke", yaml: `role: spoke`, wantRole: RoleSpoke},
		{name: "empty defaults to empty string", yaml: `{}`, wantRole: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fs FederationSection
			if err := yaml.Unmarshal([]byte(tt.yaml), &fs); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if fs.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", fs.Role, tt.wantRole)
			}
		})
	}
}

func TestFederationSection_FullSpokeParse(t *testing.T) {
	input := `
federation:
  role: spoke
  spoke_id: dc1
  hub_addr: hub.example.com:1530
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Federation.Role != RoleSpoke {
		t.Errorf("role = %q, want spoke", cfg.Federation.Role)
	}
	if cfg.Federation.SpokeID != "dc1" {
		t.Errorf("spoke_id = %q, want dc1", cfg.Federation.SpokeID)
	}
	if cfg.Federation.HubAddr != "hub.example.com:1530" {
		t.Errorf("hub_addr = %q, want hub.example.com:1530", cfg.Federation.HubAddr)
	}
}

func TestFederationSection_FullHubParse(t *testing.T) {
	input := `
federation:
  role: hub
  spokes:
    - id: dc1
      addr: spoke1.example.com:1530
    - id: dc2
      addr: spoke2.example.com:1530
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Federation.Role != RoleHub {
		t.Errorf("role = %q, want hub", cfg.Federation.Role)
	}
	if len(cfg.Federation.Spokes) != 2 {
		t.Fatalf("spokes len = %d, want 2", len(cfg.Federation.Spokes))
	}
	if cfg.Federation.Spokes[0].ID != "dc1" {
		t.Errorf("spokes[0].id = %q, want dc1", cfg.Federation.Spokes[0].ID)
	}
	if cfg.Federation.Spokes[1].Addr != "spoke2.example.com:1530" {
		t.Errorf("spokes[1].addr = %q, want spoke2.example.com:1530", cfg.Federation.Spokes[1].Addr)
	}
}

func TestFederationSection_Validate(t *testing.T) {
	tests := []struct {
		name    string
		section FederationSection
		wantErr bool
	}{
		{name: "empty is valid", section: FederationSection{}},
		{name: "standalone", section: FederationSection{Role: RoleStandalone}},
		{
			name: "hub with spokes",
			section: FederationSection{
				Role:   RoleHub,
				Spokes: []SpokeEntry{{ID: "dc1", Addr: "spoke1:1530"}},
			},
		},
		{name: "hub without spokes", section: FederationSection{Role: RoleHub}, wantErr: true},
		{
			name: "hub with spoke missing id",
			section: FederationSection{
				Role:   RoleHub,
				Spokes: []SpokeEntry{{Addr: "spoke1:1530"}},
			},
			wantErr: true,
		},
		{
			name: "hub with spoke missing addr",
			section: FederationSection{
				Role:   RoleHub,
				Spokes: []SpokeEntry{{ID: "dc1"}},
			},
			wantErr: true,
		},
		{name: "spoke with id", section: FederationSection{Role: RoleSpoke, SpokeID: "dc1"}},
		{name: "spoke without id", section: FederationSection{Role: RoleSpoke}, wantErr: true},
		{name: "invalid role", section: FederationSection{Role: "bogus"}, wantErr: true},
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

func TestFederationSection_OmittedDefaults(t *testing.T) {
	input := `
server:
  grpc_listen: ":1530"
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Federation.Role != "" {
		t.Errorf("role should be empty when omitted, got %q", cfg.Federation.Role)
	}
	if cfg.Federation.SpokeID != "" {
		t.Errorf("spoke_id should be empty when omitted, got %q", cfg.Federation.SpokeID)
	}
	if len(cfg.Federation.Spokes) != 0 {
		t.Errorf("spokes should be empty when omitted, got %d", len(cfg.Federation.Spokes))
	}
}
