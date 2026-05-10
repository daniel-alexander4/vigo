// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/conditionals"
)

func resetFleetMemo() {
	fleetMemoMu.Lock()
	fleetMemo = map[string]fleetMemoEntry{}
	fleetMemoMu.Unlock()
}

// --- detection / parsing ---

func TestAsFleetLookupConstruct(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want bool
	}{
		{"valid", map[string]any{"fleet": map[string]any{"where": "tag('web')", "select": "{{ .IP }}"}}, true},
		{"no select", map[string]any{"fleet": map[string]any{"where": "tag('web')"}}, false},
		{"no where", map[string]any{"fleet": map[string]any{"select": "{{ .IP }}"}}, false},
		{"extra top key", map[string]any{"fleet": map[string]any{"where": "x", "select": "y"}, "other": 1}, false},
		{"fleet not a map", map[string]any{"fleet": "x"}, false},
		{"not a map", "hello", false},
		{"plain map", map[string]any{"a": 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := asFleetLookupConstruct(tc.v)
			if ok != tc.want {
				t.Errorf("asFleetLookupConstruct = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestParseFleetLookup(t *testing.T) {
	good := map[string]any{"where": "tag('web') && os_family('linux')", "select": "server {{ .IP }}:80;", "join": "\n"}
	fl, err := ParseFleetLookup("backends", good)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fl.Where == "" || fl.Select == "" || fl.Join != "\n" || fl.whereExpr == nil || fl.selectTmpl == nil {
		t.Errorf("parsed FleetLookup = %+v", fl)
	}

	bad := []struct {
		name  string
		inner map[string]any
		want  string
	}{
		{"empty where", map[string]any{"where": "", "select": "x"}, "where: must be a non-empty string"},
		{"bad where syntax", map[string]any{"where": "tag('web') &&", "select": "x"}, "where:"},
		{"agent-only func in where", map[string]any{"where": "file_exists('/x')", "select": "x"}, "is not a valid selector function"},
		{"bad select template", map[string]any{"where": "tag('web')", "select": "{{ .Bogus }}"}, "select:"},
		{"unknown key", map[string]any{"where": "tag('web')", "select": "x", "bogus": 1}, "unknown key"},
		{"join not string", map[string]any{"where": "tag('web')", "select": "x", "join": 5}, "join: must be a string"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseFleetLookup("v", tc.inner)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// --- selector eval ---

func TestEvalFleetBuiltin(t *testing.T) {
	m := FleetMember{Hostname: "web1.prod.example.com", Tags: []string{"web-prod", "lb"}, Traits: map[string]string{"os.family": "debian", "os.distro": "ubuntu"}}
	traitsAny := map[string]any{"os.family": "debian", "os.distro": "ubuntu"}

	check := func(fn string, args []string, want bool) {
		t.Helper()
		got, err := evalFleetBuiltin(fn, args, m, traitsAny)
		if err != nil {
			t.Fatalf("%s(%v): %v", fn, args, err)
		}
		if got != want {
			t.Errorf("%s(%v) = %v, want %v", fn, args, got, want)
		}
	}
	check("tag", []string{"web-prod"}, true)
	check("tag", []string{"web*"}, true)
	check("tag", []string{"db"}, false)
	check("host", []string{"web*.prod.example.com"}, true)
	check("host", []string{"db*.example.com"}, false)
	check("os_family", []string{"linux"}, true)
	check("os_family", []string{"windows"}, false)
	check("distro", []string{"ubuntu"}, true)
	if _, err := evalFleetBuiltin("bogus", []string{"x"}, m, traitsAny); err == nil {
		t.Error("expected error for unknown builtin")
	}
}

func TestEvalFleetSelector_Trait(t *testing.T) {
	parse := func(s string) conditionals.WhenExpr {
		fl, err := ParseFleetLookup("v", map[string]any{"where": s, "select": "x"})
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return fl.whereExpr
	}
	ub := FleetMember{Traits: map[string]string{"os.distro": "ubuntu"}, Tags: []string{"web"}}
	cent := FleetMember{Traits: map[string]string{"os.distro": "centos"}, Tags: []string{"web"}}
	none := FleetMember{Traits: map[string]string{}, Tags: []string{"web"}}

	// trait('key', 'value') — equality on a dotted trait key (which the when:
	// lexer won't accept as a bare identifier).
	expr := parse("trait('os.distro', 'ubuntu')")
	if ok, _ := evalFleetSelector(expr, ub); !ok {
		t.Error("ubuntu should match trait('os.distro', 'ubuntu')")
	}
	if ok, _ := evalFleetSelector(expr, cent); ok {
		t.Error("centos should not match trait('os.distro', 'ubuntu')")
	}
	if ok, _ := evalFleetSelector(expr, none); ok {
		t.Error("member with no os.distro should not match")
	}

	// Combined with tag() and os_family().
	combined := parse("tag('web') && trait('os.distro', 'ubuntu') && os_family('linux')")
	ub.Traits["os.family"] = "debian"
	if ok, err := evalFleetSelector(combined, ub); err != nil || !ok {
		t.Errorf("combined selector: ok=%v err=%v, want true", ok, err)
	}
}

// --- resolution ---

func TestResolveFleetLookup_JoinAndList(t *testing.T) {
	members := []FleetMember{
		{Hostname: "a", IP: "10.0.0.3", Tags: []string{"web"}},
		{Hostname: "b", IP: "10.0.0.1", Tags: []string{"web"}},
		{Hostname: "c", IP: "10.0.0.2", Tags: []string{"db"}},
	}
	fl, err := ParseFleetLookup("v", map[string]any{"where": "tag('web')", "select": "server {{ .IP }}:80;", "join": "\n"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveFleetLookup(fl, members, nil)
	if err != nil {
		t.Fatal(err)
	}
	// sorted by rendered string: "server 10.0.0.1:80;" < "server 10.0.0.3:80;"
	want := "server 10.0.0.1:80;\nserver 10.0.0.3:80;"
	if got != want {
		t.Errorf("joined = %q, want %q", got, want)
	}

	flList, _ := ParseFleetLookup("v", map[string]any{"where": "tag('web')", "select": "{{ .Hostname }}"})
	gotList, err := resolveFleetLookup(flList, members, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, ok := gotList.([]any)
	if !ok || len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Errorf("list = %#v", gotList)
	}
}

func TestResolveFleetLookup_StaleFiltered(t *testing.T) {
	now := time.Now()
	members := []FleetMember{
		{Hostname: "a", IP: "10.0.0.1", Tags: []string{"web"}, LastSeen: now},
		{Hostname: "b", IP: "10.0.0.2", Tags: []string{"web"}, LastSeen: now.Add(-time.Hour)},
	}
	fl, _ := ParseFleetLookup("v", map[string]any{"where": "tag('web')", "select": "{{ .IP }}", "join": ","})
	isStale := func(m FleetMember) bool { return now.Sub(m.LastSeen) > time.Minute }
	got, err := resolveFleetLookup(fl, members, isStale)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.0.0.1" {
		t.Errorf("got %q, want \"10.0.0.1\" (stale b excluded)", got)
	}
	// include_stale: true keeps both.
	fl2, _ := ParseFleetLookup("v", map[string]any{"where": "tag('web')", "select": "{{ .IP }}", "join": ",", "include_stale": true})
	got2, _ := resolveFleetLookup(fl2, members, isStale)
	if got2 != "10.0.0.1,10.0.0.2" {
		t.Errorf("got %q, want \"10.0.0.1,10.0.0.2\"", got2)
	}
}

func TestResolveFleetLookup_Empty(t *testing.T) {
	fl, _ := ParseFleetLookup("v", map[string]any{"where": "tag('web')", "select": "{{ .IP }}", "join": "\n"})
	got, err := resolveFleetLookup(fl, nil, nil)
	if err != nil || got != "" {
		t.Errorf("empty fleet, join → got %q err %v, want \"\"", got, err)
	}
	flList, _ := ParseFleetLookup("v", map[string]any{"where": "tag('web')", "select": "{{ .IP }}"})
	gotList, _ := resolveFleetLookup(flList, nil, nil)
	if l, ok := gotList.([]any); !ok || len(l) != 0 {
		t.Errorf("empty fleet, no join → %#v, want empty []any", gotList)
	}
}

func TestResolveFleetLookupsInConfig_Memo(t *testing.T) {
	resetFleetMemo()
	SetFleetLookupTTL(time.Minute)
	t0 := time.Now()

	cfg := &EnvoyConfig{HasFleetLookups: true, Vars: map[string]any{}}
	fl, err := ParseFleetLookup("backends", map[string]any{"where": "tag('memo-test')", "select": "{{ .Hostname }}", "join": ","})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Vars["backends"] = fl

	calls := 0
	members := func() []FleetMember {
		calls++
		return []FleetMember{{Hostname: "h1", Tags: []string{"memo-test"}}, {Hostname: "h2", Tags: []string{"memo-test"}}}
	}

	// First resolve: miss → members() called.
	if err := ResolveFleetLookupsInConfig(cfg, members, nil, t0); err != nil {
		t.Fatal(err)
	}
	if cfg.Vars["backends"] != "h1,h2" {
		t.Errorf("backends = %v, want \"h1,h2\"", cfg.Vars["backends"])
	}
	if calls != 1 {
		t.Errorf("members() called %d times after first resolve, want 1", calls)
	}

	// Second resolve within TTL: hit → members() NOT called again.
	cfg2 := &EnvoyConfig{HasFleetLookups: true, Vars: map[string]any{"backends": fl}}
	if err := ResolveFleetLookupsInConfig(cfg2, members, nil, t0.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("members() called %d times within TTL, want still 1", calls)
	}
	if cfg2.Vars["backends"] != "h1,h2" {
		t.Errorf("cfg2 backends = %v", cfg2.Vars["backends"])
	}

	// Past the TTL: miss → members() called again.
	cfg3 := &EnvoyConfig{HasFleetLookups: true, Vars: map[string]any{"backends": fl}}
	if err := ResolveFleetLookupsInConfig(cfg3, members, nil, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("members() called %d times past TTL, want 2", calls)
	}
}

// --- integration through NewConfigStore ---

func TestForeachOverFleetVar_DeferredExpansion(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "allow.vgo"), `
name: allow
resources:
  - name: "allow-{{ .Item }}"
    type: file
    foreach: web_ips
    target_path: "/etc/allow.d/{{ .Item }}"
    content: "{{ .Item }}"
`)
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "fw01"
    modules: [allow]
    vars:
      web_ips:
        fleet:
          where: "tag('web')"
          select: "{{ .IP }}"
`)
	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	cfg := store.Get("fw01")
	if cfg == nil || !cfg.HasFleetLookups {
		t.Fatalf("expected HasFleetLookups; cfg=%v", cfg)
	}
	// At config load, the foreach resource is left unexpanded (var is a *FleetLookup).
	if _, ok := cfg.Vars["web_ips"].(*FleetLookup); !ok {
		t.Fatalf("web_ips not a *FleetLookup: %#v", cfg.Vars["web_ips"])
	}
	if len(cfg.Modules[0].Resources) != 1 || cfg.Modules[0].Resources[0].ForEach != "web_ips" {
		t.Fatalf("foreach resource should be unexpanded at load: %#v", cfg.Modules[0].Resources)
	}

	// Simulate the clone path: resolve the fleet lookup, then re-run ExpandForEach.
	resetFleetMemo()
	cloned := cfg.Clone()
	members := func() []FleetMember {
		return []FleetMember{{Hostname: "w1", IP: "10.0.0.1", Tags: []string{"web"}}, {Hostname: "w2", IP: "10.0.0.2", Tags: []string{"web"}}, {Hostname: "d1", IP: "10.0.0.9", Tags: []string{"db"}}}
	}
	if err := ResolveFleetLookupsInConfig(cloned, members, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := range cloned.Modules {
		expanded, err := ExpandForEach(cloned.Modules[i].Resources, cloned.Vars)
		if err != nil {
			t.Fatal(err)
		}
		cloned.Modules[i].Resources = expanded
	}
	got := map[string]bool{}
	for _, r := range cloned.Modules[0].Resources {
		got[r.Name] = true
	}
	for _, want := range []string{"allow-10.0.0.1", "allow-10.0.0.2"} {
		if !got[want] {
			t.Errorf("missing resource %q; have %v", want, got)
		}
	}
	if got["allow-10.0.0.9"] {
		t.Error("db host should not be in the web allow list")
	}
}

func TestForeachModule_FleetLoadError(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "stockpile", "modules", "m.vgo"), "name: m\nresources:\n  - name: r\n    type: file\n    target_path: /x\n    content: ok\n")
	writeFile(t, filepath.Join(dir, "stockpile", "default", "envoys.vgo"), `
envoys:
  - match: "h"
    modules: [m]
    vars:
      backends:
        fleet:
          where: "file_exists('/x')"
          select: "{{ .IP }}"
`)
	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected error for agent-only func in fleet: where:")
	}
	if !strings.Contains(err.Error(), "is not a valid selector function") {
		t.Errorf("error = %q", err.Error())
	}
}
