// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigogit

import (
	"strings"
	"testing"
)

// minimalSidecar returns a valid minimum-field sidecar matching the
// Rust suite's `minimal()` helper in `agent/src/vigogit/sidecar.rs`.
// Tests mutate this string to exercise individual failure modes.
func minimalSidecar() string {
	return "owner: danlap\n" +
		"max_mirrors: 5\n" +
		"tags: []\n" +
		"paused: false\n" +
		"description: \"\"\n"
}

func TestParsesMinimalValid(t *testing.T) {
	s, clamped, err := LoadSidecar([]byte(minimalSidecar()))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if clamped {
		t.Fatalf("expected clamped=false, got true")
	}
	if s.Owner != "danlap" {
		t.Errorf("owner = %q, want danlap", s.Owner)
	}
	if s.MaxMirrors != 5 {
		t.Errorf("max_mirrors = %d, want 5", s.MaxMirrors)
	}
	if len(s.Tags) != 0 {
		t.Errorf("tags = %v, want empty", s.Tags)
	}
	if s.Paused {
		t.Error("paused = true, want false")
	}
	if s.Description != "" {
		t.Errorf("description = %q, want empty", s.Description)
	}
}

func TestParsesFullValid(t *testing.T) {
	yaml := `owner: homeserver
max_mirrors: 7
tags: [personal, notes, work]
paused: true
description: "a test repo"
`
	s, clamped, err := LoadSidecar([]byte(yaml))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if clamped {
		t.Errorf("clamped = true, want false")
	}
	if s.Owner != "homeserver" {
		t.Errorf("owner = %q", s.Owner)
	}
	if s.MaxMirrors != 7 {
		t.Errorf("max_mirrors = %d", s.MaxMirrors)
	}
	if len(s.Tags) != 3 {
		t.Fatalf("tags len = %d", len(s.Tags))
	}
	if !s.Paused {
		t.Error("paused = false")
	}
	if s.Description != "a test repo" {
		t.Errorf("description = %q", s.Description)
	}
}

func TestClampsMaxMirrorsLow(t *testing.T) {
	yaml := strings.Replace(minimalSidecar(), "max_mirrors: 5", "max_mirrors: 1", 1)
	s, clamped, err := LoadSidecar([]byte(yaml))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if !clamped {
		t.Errorf("expected clamped=true")
	}
	if s.MaxMirrors != MinMirrors {
		t.Errorf("max_mirrors = %d, want %d", s.MaxMirrors, MinMirrors)
	}
}

func TestClampsMaxMirrorsHigh(t *testing.T) {
	yaml := strings.Replace(minimalSidecar(), "max_mirrors: 5", "max_mirrors: 100", 1)
	s, clamped, err := LoadSidecar([]byte(yaml))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if !clamped {
		t.Errorf("expected clamped=true")
	}
	if s.MaxMirrors != MaxMirrors {
		t.Errorf("max_mirrors = %d, want %d", s.MaxMirrors, MaxMirrors)
	}
}

func TestRejectsNegativeMaxMirrors(t *testing.T) {
	yaml := strings.Replace(minimalSidecar(), "max_mirrors: 5", "max_mirrors: -1", 1)
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	se, ok := err.(*SidecarError)
	if !ok {
		t.Fatalf("expected *SidecarError, got %T", err)
	}
	if se.Kind != "invalid_mirrors" {
		t.Errorf("kind = %q, want invalid_mirrors", se.Kind)
	}
}

func TestRejectsUnknownField(t *testing.T) {
	yaml := minimalSidecar() + "retention_days: 90\n"
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	se, ok := err.(*SidecarError)
	if !ok {
		t.Fatalf("expected *SidecarError, got %T", err)
	}
	if se.Kind != "unknown_field" {
		t.Errorf("kind = %q, want unknown_field", se.Kind)
	}
}

func TestRejectsMissingOwner(t *testing.T) {
	yaml := "max_mirrors: 5\ntags: []\npaused: false\ndescription: \"\"\n"
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	se := err.(*SidecarError)
	if se.Kind != "missing_field" {
		t.Errorf("kind = %q", se.Kind)
	}
	if !strings.Contains(se.Message, "owner") {
		t.Errorf("message = %q, want to mention owner", se.Message)
	}
}

func TestRejectsMissingMaxMirrors(t *testing.T) {
	yaml := "owner: danlap\ntags: []\npaused: false\ndescription: \"\"\n"
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.(*SidecarError).Kind != "missing_field" {
		t.Errorf("kind = %q", err.(*SidecarError).Kind)
	}
}

func TestRejectsMissingTags(t *testing.T) {
	yaml := "owner: danlap\nmax_mirrors: 5\npaused: false\ndescription: \"\"\n"
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil || err.(*SidecarError).Kind != "missing_field" {
		t.Fatalf("expected missing_field, got %v", err)
	}
}

func TestRejectsMissingPaused(t *testing.T) {
	yaml := "owner: danlap\nmax_mirrors: 5\ntags: []\ndescription: \"\"\n"
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil || err.(*SidecarError).Kind != "missing_field" {
		t.Fatalf("expected missing_field, got %v", err)
	}
}

func TestRejectsMissingDescription(t *testing.T) {
	yaml := "owner: danlap\nmax_mirrors: 5\ntags: []\npaused: false\n"
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil || err.(*SidecarError).Kind != "missing_field" {
		t.Fatalf("expected missing_field, got %v", err)
	}
}

func TestRejectsInvalidOwnerHostname(t *testing.T) {
	bad := []string{
		"owner: foo_bar",          // underscore
		"owner: -foo",             // leading dash
		"owner: foo..bar",         // empty label
		"owner: UPPERCASE",        // ownership is lowercased first; UPPER → lower ok
		"owner: \"../etc/passwd\"", // path traversal
	}
	// UPPERCASE is actually VALID after lowercase normalization, so
	// drop it from the reject list.
	for _, prefix := range bad {
		if strings.Contains(prefix, "UPPERCASE") {
			continue
		}
		yaml := strings.Replace(minimalSidecar(), "owner: danlap", prefix, 1)
		_, _, err := LoadSidecar([]byte(yaml))
		if err == nil {
			t.Errorf("expected error for %q", prefix)
			continue
		}
		se, ok := err.(*SidecarError)
		if !ok {
			t.Errorf("wrong error type for %q: %T", prefix, err)
			continue
		}
		if se.Kind != "invalid_owner" {
			t.Errorf("kind for %q = %q, want invalid_owner", prefix, se.Kind)
		}
	}
}

func TestAcceptsUppercaseOwnerViaLowercase(t *testing.T) {
	yaml := strings.Replace(minimalSidecar(), "owner: danlap", "owner: DANLAP", 1)
	s, _, err := LoadSidecar([]byte(yaml))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if s.Owner != "danlap" {
		t.Errorf("owner = %q, want lowercased danlap", s.Owner)
	}
}

func TestAcceptsFqdnOwner(t *testing.T) {
	yaml := strings.Replace(minimalSidecar(), "owner: danlap", "owner: box1.example.com", 1)
	if _, _, err := LoadSidecar([]byte(yaml)); err != nil {
		t.Fatalf("expected ok for FQDN, got %v", err)
	}
}

func TestRejectsOversizedFile(t *testing.T) {
	big := make([]byte, MaxSidecarBytes+1)
	for i := range big {
		big[i] = ' '
	}
	_, _, err := LoadSidecar(big)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.(*SidecarError).Kind != "too_large" {
		t.Errorf("kind = %q", err.(*SidecarError).Kind)
	}
}

func TestRejectsTooManyTags(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("owner: danlap\nmax_mirrors: 5\ntags: [")
	for i := 0; i < MaxTags+1; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("t")
	}
	sb.WriteString("]\npaused: false\ndescription: \"\"\n")
	_, _, err := LoadSidecar([]byte(sb.String()))
	if err == nil || err.(*SidecarError).Kind != "too_many_tags" {
		t.Fatalf("expected too_many_tags, got %v", err)
	}
}

func TestRejectsTooLongTag(t *testing.T) {
	longTag := strings.Repeat("x", MaxTagChars+1)
	yaml := strings.Replace(minimalSidecar(), "tags: []", "tags: ["+longTag+"]", 1)
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil || err.(*SidecarError).Kind != "invalid_tag" {
		t.Fatalf("expected invalid_tag, got %v", err)
	}
}

func TestRejectsEmptyTag(t *testing.T) {
	yaml := strings.Replace(minimalSidecar(), "tags: []", "tags: [\"\"]", 1)
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil || err.(*SidecarError).Kind != "invalid_tag" {
		t.Fatalf("expected invalid_tag, got %v", err)
	}
}

func TestRejectsTooLongDescription(t *testing.T) {
	longDesc := strings.Repeat("x", MaxDescriptionChars+1)
	yaml := strings.Replace(minimalSidecar(), "description: \"\"", "description: \""+longDesc+"\"", 1)
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil || err.(*SidecarError).Kind != "description_too_long" {
		t.Fatalf("expected description_too_long, got %v", err)
	}
}

func TestAcceptsDescriptionAtCap(t *testing.T) {
	exact := strings.Repeat("x", MaxDescriptionChars)
	yaml := strings.Replace(minimalSidecar(), "description: \"\"", "description: \""+exact+"\"", 1)
	if _, _, err := LoadSidecar([]byte(yaml)); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestRejectsMalformedYaml(t *testing.T) {
	yaml := "owner: danlap\nmax_mirrors: [this is a list not an int]\ntags: []\npaused: false\ndescription: \"\"\n"
	_, _, err := LoadSidecar([]byte(yaml))
	if err == nil {
		t.Fatal("expected error")
	}
	if err.(*SidecarError).Kind != "parse" {
		t.Errorf("kind = %q, want parse", err.(*SidecarError).Kind)
	}
}

// TestHostnamePatternMatchesRust exercises the hostname validator
// against the same positive and negative vectors as the Rust
// `is_valid_hostname` test cases in `agent/src/vigogit/sidecar.rs`.
// Cross-language parity is a correctness requirement — a hostname
// accepted by one side and rejected by the other is a bug.
func TestHostnamePatternMatchesRust(t *testing.T) {
	valid := []string{
		"danlap", "home-server", "box-1", "a",
		"danlap.local", "homeserver.lan", "box1.example.com",
	}
	for _, v := range valid {
		if !isValidHostname(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}
	invalid := []string{
		"", ".", ".foo", "foo.", "foo..bar",
		"-foo", "foo_bar", "Foo",
		strings.Repeat("a", MaxHostnameBytes+1),
		strings.Repeat("a", MaxHostnameLabelBytes+1),
	}
	for _, v := range invalid {
		if isValidHostname(v) {
			t.Errorf("expected %q to be invalid", v)
		}
	}
}

func TestUtf8CharCountMatchesRustChars(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"abc":    3,
		"héllo":  5, // é is one code point
		"a\u0301": 2, // "a" + combining acute = 2 code points
	}
	for s, want := range cases {
		if got := utf8CharCount(s); got != want {
			t.Errorf("utf8CharCount(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestSplitDotMatchesExpected(t *testing.T) {
	cases := map[string][]string{
		"":            nil,
		"foo":         {"foo"},
		"foo.bar":     {"foo", "bar"},
		"foo.bar.baz": {"foo", "bar", "baz"},
		".foo":        {"", "foo"},
		"foo.":        {"foo", ""},
	}
	for s, want := range cases {
		got := splitDot(s)
		if len(got) != len(want) {
			t.Errorf("splitDot(%q) = %v, want %v", s, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("splitDot(%q)[%d] = %q, want %q", s, i, got[i], want[i])
			}
		}
	}
}
