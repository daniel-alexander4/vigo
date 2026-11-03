// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"

	"git.alexander4.io/vigo/server/fleet"
)

// TestResolveByTraitFallbackMatchesArrayLeaf verifies that after byTrait stops
// indexing array-element leaves, resolveByTrait still resolves a key=value
// selector on such a leaf via the on-demand scan of the flattened traits map.
func TestResolveByTraitFallbackMatchesArrayLeaf(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID: "t1", Hostname: "h1", ConvergenceStatus: "no data",
		Traits: map[string]string{"os.distro": "ubuntu", "cves.0.id": "CVE-2024-0001"},
	}, nil)
	fi.AddEnvoy(fleet.EnvoyState{
		ID: "t2", Hostname: "h2", ConvergenceStatus: "no data",
		Traits: map[string]string{"os.distro": "debian", "cves.0.id": "CVE-2024-9999"},
	}, nil)

	// Stable key — resolved by the byTrait fast path.
	got, err := resolveByTrait(context.Background(), nil, "os.distro=ubuntu", fi)
	if err != nil || len(got) != 1 || got[0].EnvoyID != "t1" {
		t.Fatalf("fast path os.distro=ubuntu: got %v err %v, want [t1]", got, err)
	}

	// Array-element leaf — NOT in byTrait, resolved by the on-demand fallback.
	got, err = resolveByTrait(context.Background(), nil, "cves.0.id=CVE-2024-0001", fi)
	if err != nil || len(got) != 1 || got[0].EnvoyID != "t1" {
		t.Fatalf("fallback cves.0.id: got %v err %v, want [t1]", got, err)
	}

	// No match anywhere — still errors.
	if _, err := resolveByTrait(context.Background(), nil, "cves.0.id=CVE-NONE", fi); err == nil {
		t.Fatalf("expected error for non-matching array-leaf selector")
	}
}

func TestJsonPointerLookup(t *testing.T) {
	data := map[string]any{
		"os": map[string]any{
			"distro": "ubuntu",
			"family": "debian",
		},
		"hostname": "web01.prod",
	}

	tests := []struct {
		pointer string
		want    any
	}{
		{"/os/distro", "ubuntu"},
		{"/os/family", "debian"},
		{"/hostname", "web01.prod"},
		{"/os/nonexistent", nil},
		{"/nonexistent", nil},
		{"/os/distro/deep", nil},
	}

	for _, tt := range tests {
		got := jsonPointerLookup(data, tt.pointer)
		if got != tt.want {
			t.Errorf("jsonPointerLookup(%q) = %v, want %v", tt.pointer, got, tt.want)
		}
	}
}

func TestJsonPointerLookupNumeric(t *testing.T) {
	data := map[string]any{
		"memory": map[string]any{
			"total_mb": float64(8192),
		},
	}

	got := jsonPointerLookup(data, "/memory/total_mb")
	if got != float64(8192) {
		t.Errorf("expected 8192, got %v", got)
	}
}
