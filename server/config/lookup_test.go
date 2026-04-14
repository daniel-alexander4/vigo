// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"reflect"
	"testing"
)

func TestMatchEnvoy(t *testing.T) {
	hostname := "web01.prod.example.com"
	tags := []string{"webserver", "production"}
	traits := map[string]string{
		"os.distro": "ubuntu",
		"os.arch":   "amd64",
	}

	cases := []struct {
		spec string
		want bool
	}{
		// Exact hostname
		{"web01.prod.example.com", true},
		{"web02.prod.example.com", false},
		// Hostname glob
		{"*.prod.example.com", true},
		{"*.staging.example.com", false},
		{"web??.prod.example.com", true},
		// Tag
		{"tag:webserver", true},
		{"tag:database", false},
		// Tag glob
		{"tag:web*", true},
		{"tag:*server", true},
		{"tag:db*", false},
		// Trait filter
		{"os.distro=ubuntu", true},
		{"os.distro=debian", false},
		{"os.arch=amd64", true},
		{"missing.key=value", false},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := MatchEnvoy(tc.spec, hostname, tags, traits)
			if err != nil {
				t.Fatalf("MatchEnvoy(%q): %v", tc.spec, err)
			}
			if got != tc.want {
				t.Errorf("MatchEnvoy(%q) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}

func TestResolveLookup_Lists_Union(t *testing.T) {
	// danlap is tagged workstation; lookup should union both matching arms.
	table := map[string]any{
		"tag:workstation": []any{"docker", "virtualbox"},
		"tag:mediaserver": []any{"docker", "plex"},
		"danlap":          []any{"kvm"},
		"default":         []any{},
	}
	// Pass an explicit keyOrder — map iteration order is non-deterministic
	// in Go, so the union's declaration-order tiebreak depends on the caller
	// providing a stable order.
	keyOrder := []string{"tag:workstation", "tag:mediaserver", "danlap", "default"}
	got, err := ResolveLookup(table, keyOrder, "danlap", []string{"workstation"}, nil)
	if err != nil {
		t.Fatalf("ResolveLookup: %v", err)
	}
	gotList, ok := got.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", got)
	}
	// Should contain docker, virtualbox, kvm (union of workstation + danlap arms).
	// Order: declaration order of matching arms.
	want := []any{"docker", "virtualbox", "kvm"}
	if !reflect.DeepEqual(gotList, want) {
		t.Errorf("got %v, want %v", gotList, want)
	}
}

func TestResolveLookup_Lists_NoMatchUsesDefault(t *testing.T) {
	table := map[string]any{
		"tag:workstation": []any{"virtualbox"},
		"default":         []any{"docker"},
	}
	got, err := ResolveLookup(table, nil, "nginx", []string{"webserver"}, nil)
	if err != nil {
		t.Fatalf("ResolveLookup: %v", err)
	}
	want := []any{"docker"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveLookup_Scalars_MostSpecificWins(t *testing.T) {
	// plex is a mediaserver, danlap is also a workstation. Test that
	// the more specific match wins regardless of file order.
	table := map[string]any{
		"tag:workstation": "/bin/zsh",
		"danlap":          "/bin/fish",
		"default":         "/bin/bash",
	}
	// danlap hostname + workstation tag → exact hostname wins over tag.
	got, err := ResolveLookup(table, []string{"tag:workstation", "danlap", "default"},
		"danlap", []string{"workstation"}, nil)
	if err != nil {
		t.Fatalf("ResolveLookup: %v", err)
	}
	if got != "/bin/fish" {
		t.Errorf("got %v, want /bin/fish", got)
	}

	// plex hostname, only workstation tag NOT set → tag doesn't match,
	// default wins.
	got, err = ResolveLookup(table, []string{"tag:workstation", "danlap", "default"},
		"plex", []string{"mediaserver"}, nil)
	if err != nil {
		t.Fatalf("ResolveLookup: %v", err)
	}
	if got != "/bin/bash" {
		t.Errorf("got %v, want /bin/bash", got)
	}
}

func TestResolveLookup_Scalars_OrderIndependence(t *testing.T) {
	// Specificity ordering should make file order irrelevant for scalars.
	// Here danlap is listed FIRST but workstation tag is still more general,
	// so danlap still wins.
	table := map[string]any{
		"danlap":          "/bin/fish",
		"tag:workstation": "/bin/zsh",
		"default":         "/bin/bash",
	}
	for _, keyOrder := range [][]string{
		{"danlap", "tag:workstation", "default"},
		{"tag:workstation", "danlap", "default"},
		{"default", "danlap", "tag:workstation"},
	} {
		got, err := ResolveLookup(table, keyOrder, "danlap", []string{"workstation"}, nil)
		if err != nil {
			t.Fatalf("keyOrder=%v: %v", keyOrder, err)
		}
		if got != "/bin/fish" {
			t.Errorf("keyOrder=%v: got %v, want /bin/fish", keyOrder, got)
		}
	}
}

func TestResolveLookup_MissingDefault(t *testing.T) {
	table := map[string]any{
		"tag:workstation": []any{"virtualbox"},
	}
	_, err := ResolveLookup(table, nil, "danlap", []string{"workstation"}, nil)
	if err == nil {
		t.Fatal("expected error for missing default, got nil")
	}
}

func TestResolveLookup_MixedTypes(t *testing.T) {
	table := map[string]any{
		"tag:workstation": []any{"virtualbox"}, // list
		"danlap":          "fish",              // scalar — mismatch
		"default":         []any{},
	}
	_, err := ResolveLookup(table, nil, "danlap", []string{"workstation"}, nil)
	if err == nil {
		t.Fatal("expected error for mixed-type arms, got nil")
	}
}

func TestResolveLookup_TraitFilter(t *testing.T) {
	table := map[string]any{
		"os.distro=ubuntu": []any{"snapd"},
		"os.distro=debian": []any{"dpkg-utils"},
		"default":          []any{},
	}
	traits := map[string]string{"os.distro": "ubuntu"}
	got, err := ResolveLookup(table, nil, "any", nil, traits)
	if err != nil {
		t.Fatalf("ResolveLookup: %v", err)
	}
	want := []any{"snapd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestIsLookupTable(t *testing.T) {
	if !IsLookupTable(map[string]any{"default": "x"}) {
		t.Error("map with default should be a lookup table")
	}
	if IsLookupTable(map[string]any{"foo": "bar"}) {
		t.Error("map without default should not be a lookup table")
	}
	if IsLookupTable("scalar") {
		t.Error("scalar should not be a lookup table")
	}
	if IsLookupTable([]any{"default"}) {
		t.Error("list should not be a lookup table")
	}
}

func TestResolveLookupsInAttributes(t *testing.T) {
	attrs := map[string]any{
		"name":     "dan",
		"username": "dan",
		"groups": map[string]any{
			"tag:workstation": []any{"docker", "virtualbox"},
			"tag:mediaserver": []any{"docker", "plex"},
			"default":         []any{"docker"},
		},
		"shell": map[string]any{
			"danlap":  "/bin/fish",
			"default": "/bin/bash",
		},
	}
	err := ResolveLookupsInAttributes(attrs, "danlap", []string{"workstation"}, nil)
	if err != nil {
		t.Fatalf("ResolveLookupsInAttributes: %v", err)
	}

	// Literal attrs unchanged.
	if attrs["name"] != "dan" {
		t.Errorf("name mutated: %v", attrs["name"])
	}

	// groups: lookup should have been resolved to [docker, virtualbox].
	gotGroups, ok := attrs["groups"].([]any)
	if !ok {
		t.Fatalf("groups not resolved to list: %T", attrs["groups"])
	}
	wantGroups := []any{"docker", "virtualbox"}
	if !reflect.DeepEqual(gotGroups, wantGroups) {
		t.Errorf("groups = %v, want %v", gotGroups, wantGroups)
	}

	// shell: lookup should have been resolved to /bin/fish.
	if attrs["shell"] != "/bin/fish" {
		t.Errorf("shell = %v, want /bin/fish", attrs["shell"])
	}
}
