// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package validate

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// rustConstList returns the string literals of `const NAME: &[&str] = &[...]`
// in src, for every cfg variant of that name, in source order.
func rustConstList(t *testing.T, src, name, cfg string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?s)#\[cfg\(` + cfg + `\)\]\s*const ` + name + `: &\[&str\] = &\[(.*?)\];`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("const %s (cfg %s) not found in agent/src/guardrails.rs", name, cfg)
	}
	var out []string
	for _, lit := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, lit[1])
	}
	return out
}

// TestGuardrailListsMatchAgent pins the server's publish-time guardrail
// preview to the agent's lists, which are the authority.
func TestGuardrailListsMatchAgent(t *testing.T) {
	data, err := os.ReadFile("../../agent/src/guardrails.rs")
	if err != nil {
		t.Skipf("agent source not present: %v", err)
	}
	src := string(data)
	if got, want := protectedPaths, rustConstList(t, src, "PROTECTED_PATHS", "unix"); !reflect.DeepEqual(got, want) {
		t.Errorf("protectedPaths drifted from agent PROTECTED_PATHS:\n got %q\nwant %q", got, want)
	}
	if got, want := exemptPaths, rustConstList(t, src, "EXEMPT_PATHS", "unix"); !reflect.DeepEqual(got, want) {
		t.Errorf("exemptPaths drifted from agent EXEMPT_PATHS:\n got %q\nwant %q", got, want)
	}
	want := append(rustConstList(t, src, "DANGEROUS_PATTERNS", "unix"), rustConstList(t, src, "DANGEROUS_PATTERNS", "windows")...)
	if !reflect.DeepEqual(dangerousPatterns, want) {
		t.Errorf("dangerousPatterns drifted from agent DANGEROUS_PATTERNS:\n got %q\nwant %q", dangerousPatterns, want)
	}
}

// TestProtectedPathMatchCleansTarget: `//` and `/./` cannot dodge the
// protected-path match; the exempt scan dir is not flagged.
func TestProtectedPathMatchCleansTarget(t *testing.T) {
	for _, p := range []string{"/etc//vigo/x", "/etc/./vigo/x", "/srv/vigo/../vigo/s"} {
		if matchProtectedPath(p) == "" {
			t.Errorf("matchProtectedPath(%q) = \"\", want a protected match", p)
		}
	}
	if m := matchProtectedPath("/var/lib/vigo/security/scan.json"); m != "" {
		t.Errorf("exempt path flagged as %q", m)
	}
}

// TestPushCommandWhitespaceNormalized: extra whitespace or doubled slashes
// do not defeat the dangerous-pattern scan.
func TestPushCommandWhitespaceNormalized(t *testing.T) {
	for _, c := range []string{"systemctl  stop vigo", "systemctl\tstop  vigo-envoy", "rm -rf //srv//vigo"} {
		if err := ValidatePushCommand(c); err == nil {
			t.Errorf("ValidatePushCommand(%q) = nil, want a violation", strings.ReplaceAll(c, "\t", `\t`))
		}
	}
}
