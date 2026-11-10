// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestServerConfigSchema_DocTagsPresent is the guardrail that enforces
// "every server.yaml field has a one-line description". The Server Config
// UI page reflects the ServerConfig struct at runtime — if a new field ships
// without a doc tag, the page shows a blank row. This test fails in CI
// instead.
//
// The test walks every exported struct field on ServerConfig and every
// struct that is reachable through it, asserting that each field either has a
// `doc:` tag or is explicitly exempt. If you're adding a new field and this
// test fails, add a short description to the struct tag (not a comment).
func TestServerConfigSchema_DocTagsPresent(t *testing.T) {
	visited := map[reflect.Type]bool{}
	var missing []string
	walkForDocTags(reflect.TypeOf(ServerConfig{}), "", visited, &missing)

	if len(missing) > 0 {
		t.Fatalf("ServerConfig fields missing `doc:` struct tag — add a one-line description to each:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func walkForDocTags(t reflect.Type, prefix string, visited map[reflect.Type]bool, missing *[]string) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	if visited[t] {
		return
	}
	visited[t] = true

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := yamlFieldName(f)
		if name == "" || name == "-" {
			continue
		}
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}

		if f.Tag.Get("doc") == "" {
			*missing = append(*missing, full+"  ("+t.Name()+"."+f.Name+")")
		}

		// Recurse into nested structs / pointer-to-struct / slices of struct.
		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			walkForDocTags(ft, full, visited, missing)
		}
	}
}

// TestServerConfigSchema_BuildsAllSections sanity-checks that the reflection
// walker enumerates every top-level section and that known sections are
// present. Guards against regressions where a refactor silently drops a
// section from the UI.
func TestServerConfigSchema_BuildsAllSections(t *testing.T) {
	schema := ServerConfigSchema()
	if len(schema) == 0 {
		t.Fatal("ServerConfigSchema returned no sections")
	}

	// Spot-check a handful of sections the UI depends on.
	expect := []string{"server", "database", "auth", "swarm", "integrations", "ai", "scrier"}
	got := make(map[string]bool, len(schema))
	for _, s := range schema {
		got[s.Name] = true
	}
	for _, want := range expect {
		if !got[want] {
			t.Errorf("schema missing expected section %q", want)
		}
	}

	// Every section's Description must be non-empty (it comes from the
	// `doc:` tag on the ServerConfig field itself).
	for _, s := range schema {
		if s.Description == "" {
			t.Errorf("section %q has empty description — add doc:\"...\" to the ServerConfig field", s.Name)
		}
	}
}

// TestServerConfigSchema_IntegrationsHasSubsections confirms the recursion
// descends one level — integrations is the load-bearing case because every
// integration is a pointer-to-struct subsection.
func TestServerConfigSchema_IntegrationsHasSubsections(t *testing.T) {
	sec := FindSchemaSection("integrations")
	if sec == nil {
		t.Fatal("integrations section missing from schema")
	}
	if len(sec.Subsections) == 0 {
		t.Fatal("integrations.Subsections is empty — pointer-to-struct recursion broken")
	}
	names := map[string]bool{}
	for _, sub := range sec.Subsections {
		names[sub.Name] = true
	}
	for _, want := range []string{"slack", "pagerduty", "jira"} {
		if !names[want] {
			t.Errorf("integrations.Subsections missing %q", want)
		}
	}
}

// TestReadServerYAMLState exercises the raw-YAML presence detector against a
// fixture that exhibits every state the detector must distinguish:
// present+enabled, present+explicitly-disabled, present+empty-map,
// present+null, and absent (only one section is set).
func TestReadServerYAMLState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	yml := `
server:
  hostname: vigo.example.com

# database is commented out — should be absent

auth: {}

swarm:
  enabled: false
  port: 1531

ai:
  enabled: true
  provider: claude
  api_key: "secret:vigo/ai/key"

scrier: null
`
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	state, err := ReadServerYAMLState(path)
	if err != nil {
		t.Fatalf("ReadServerYAMLState: %v", err)
	}

	cases := []struct {
		name        string
		wantPresent bool
		wantEnabled bool
	}{
		{"server", true, true},
		{"database", false, false}, // commented out
		{"auth", true, false},      // empty mapping
		{"swarm", true, false},     // enabled: false
		{"ai", true, true},         // enabled: true
		{"scrier", false, false},   // explicit null
	}
	for _, tc := range cases {
		got := state.Section(tc.name)
		if tc.wantPresent {
			if got == nil || !got.Present {
				t.Errorf("section %q: want Present=true, got %+v", tc.name, got)
				continue
			}
		} else {
			if got != nil && got.Present {
				t.Errorf("section %q: want Present=false, got %+v", tc.name, got)
			}
		}
		if got != nil && got.Enabled != tc.wantEnabled {
			t.Errorf("section %q: want Enabled=%v, got Enabled=%v", tc.name, tc.wantEnabled, got.Enabled)
		}
	}

	// EnabledCount should match the "enabled" cases above (server + ai = 2).
	if n := state.EnabledCount(); n != 2 {
		t.Errorf("EnabledCount = %d, want 2", n)
	}

	// The AI api_key must render as the literal secret: ref, never resolved.
	ai := state.Section("ai")
	if ai == nil || !ai.Enabled {
		t.Fatalf("ai section not enabled in fixture: %+v", ai)
	}
	got := ai.FieldValue("api_key")
	if got != "secret:vigo/ai/key" {
		t.Errorf("ai.api_key FieldValue = %q, want %q (secret refs must pass through verbatim)", got, "secret:vigo/ai/key")
	}
}

// TestReadServerYAMLState_Subsection confirms the nested lookup used for
// integrations.* detail pages — raw YAML is walked, so "integrations.slack"
// with enabled: true shows up correctly even though the parent is a
// pointer-to-struct in Go.
func TestReadServerYAMLState_Subsection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	yml := `
integrations:
  slack:
    enabled: true
    webhook_url: "secret:vigo/slack/webhook"
  jira:
    enabled: false
`
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state, err := ReadServerYAMLState(path)
	if err != nil {
		t.Fatalf("ReadServerYAMLState: %v", err)
	}

	integ := state.Section("integrations")
	if integ == nil || !integ.Enabled {
		t.Fatalf("integrations should be enabled: %+v", integ)
	}
	slack := integ.SubsectionState("slack")
	if slack == nil || !slack.Enabled {
		t.Errorf("integrations.slack should be enabled: %+v", slack)
	}
	jira := integ.SubsectionState("jira")
	if jira == nil || jira.Enabled {
		t.Errorf("integrations.jira should be present-but-disabled (enabled: false): %+v", jira)
	}
	if got := slack.FieldValue("webhook_url"); got != "secret:vigo/slack/webhook" {
		t.Errorf("slack.webhook_url = %q, want literal secret: ref", got)
	}
}

// TestReadServerYAMLState_MissingFile reports a clean error instead of
// panicking.
func TestReadServerYAMLState_MissingFile(t *testing.T) {
	_, err := ReadServerYAMLState("/no/such/path/server.yaml")
	if err == nil {
		t.Fatal("expected error reading missing file, got nil")
	}
}

// TestReadServerYAMLState_EmptyFile handles an empty or whitespace-only file
// gracefully — no sections, no panic.
func TestReadServerYAMLState_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state, err := ReadServerYAMLState(path)
	if err != nil {
		t.Fatalf("ReadServerYAMLState: %v", err)
	}
	if len(state.Sections) != 0 {
		t.Errorf("expected 0 sections, got %d", len(state.Sections))
	}
}
