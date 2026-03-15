// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSpannerRole_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantRole SpannerRole
	}{
		{name: "standalone", yaml: `role: standalone`, wantRole: RoleStandalone},
		{name: "hub", yaml: `role: hub`, wantRole: RoleHub},
		{name: "spoke", yaml: `role: spoke`, wantRole: RoleSpoke},
		{name: "empty defaults to empty string", yaml: `{}`, wantRole: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fs SpannerSection
			if err := yaml.Unmarshal([]byte(tt.yaml), &fs); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if fs.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", fs.Role, tt.wantRole)
			}
		})
	}
}

func TestSpannerSection_FullSpokeParse(t *testing.T) {
	input := `
spanner:
  role: spoke
  spoke_id: dc1
  hub_addr: hub.example.com:1530
`
	var cfg ServerConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Spanner.Role != RoleSpoke {
		t.Errorf("role = %q, want spoke", cfg.Spanner.Role)
	}
	if cfg.Spanner.SpokeID != "dc1" {
		t.Errorf("spoke_id = %q, want dc1", cfg.Spanner.SpokeID)
	}
	if cfg.Spanner.HubAddr != "hub.example.com:1530" {
		t.Errorf("hub_addr = %q, want hub.example.com:1530", cfg.Spanner.HubAddr)
	}
}

func TestSpannerSection_FullHubParse(t *testing.T) {
	input := `
spanner:
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

	if cfg.Spanner.Role != RoleHub {
		t.Errorf("role = %q, want hub", cfg.Spanner.Role)
	}
	if len(cfg.Spanner.Spokes) != 2 {
		t.Fatalf("spokes len = %d, want 2", len(cfg.Spanner.Spokes))
	}
	if cfg.Spanner.Spokes[0].ID != "dc1" {
		t.Errorf("spokes[0].id = %q, want dc1", cfg.Spanner.Spokes[0].ID)
	}
	if cfg.Spanner.Spokes[1].Addr != "spoke2.example.com:1530" {
		t.Errorf("spokes[1].addr = %q, want spoke2.example.com:1530", cfg.Spanner.Spokes[1].Addr)
	}
}

func TestSpannerSection_Validate(t *testing.T) {
	tests := []struct {
		name    string
		section SpannerSection
		wantErr bool
	}{
		{name: "empty is valid", section: SpannerSection{}},
		{name: "standalone", section: SpannerSection{Role: RoleStandalone}},
		{
			name: "hub with spokes",
			section: SpannerSection{
				Role:   RoleHub,
				Spokes: []SpokeEntry{{ID: "dc1", Addr: "spoke1:1530"}},
			},
		},
		{name: "hub without spokes", section: SpannerSection{Role: RoleHub}, wantErr: true},
		{
			name: "hub with spoke missing id",
			section: SpannerSection{
				Role:   RoleHub,
				Spokes: []SpokeEntry{{Addr: "spoke1:1530"}},
			},
			wantErr: true,
		},
		{
			name: "hub with spoke missing addr",
			section: SpannerSection{
				Role:   RoleHub,
				Spokes: []SpokeEntry{{ID: "dc1"}},
			},
			wantErr: true,
		},
		{name: "spoke with id", section: SpannerSection{Role: RoleSpoke, SpokeID: "dc1"}},
		{name: "spoke without id", section: SpannerSection{Role: RoleSpoke}, wantErr: true},
		{name: "invalid role", section: SpannerSection{Role: "bogus"}, wantErr: true},
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

	if cfg.Spanner.Role != "" {
		t.Errorf("role should be empty when omitted, got %q", cfg.Spanner.Role)
	}
	if cfg.Spanner.SpokeID != "" {
		t.Errorf("spoke_id should be empty when omitted, got %q", cfg.Spanner.SpokeID)
	}
	if len(cfg.Spanner.Spokes) != 0 {
		t.Errorf("spokes should be empty when omitted, got %d", len(cfg.Spanner.Spokes))
	}
}
