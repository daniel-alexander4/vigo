// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExtractCVEID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"danlap · cve CVE-2024-1234 (high)", "CVE-2024-1234"},
		{"danlap · cve cve-2024-0001 (low)", "CVE-2024-0001"},
		{"danlap · trivy CVE-2023-0001 (high): libssl 1.0", "CVE-2023-0001"},
		{"h · debsecan CVE-2024-9999999 (medium): nginx", "CVE-2024-9999999"},
		// First match wins when multiple appear (helper format puts the
		// canonical ID first, then the cve.org URL repeats it).
		{"h · cve CVE-2024-0001 (high) (https://www.cve.org/CVERecord?id=CVE-2024-0001)", "CVE-2024-0001"},
		// Sub-4-digit suffix is not a valid CVE ID — skip it rather than
		// treating "CVE-2024-1" as a hit.
		{"some non-CVE text with CVE-2024-1 in it", ""},
		// Free-text without an ID returns empty.
		{"danlap · lynis-warn AUTH-9286: weak password fields", ""},
		{"how many envoys do I have?", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractCVEID(tt.in)
		if got != tt.want {
			t.Errorf("extractCVEID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatCVEDetailMarkdown(t *testing.T) {
	d := &CVEDetail{
		CVEID:       "CVE-2024-1234",
		CVSSScore:   7.5,
		CVSSVector:  "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H",
		Severity:    "high",
		Description: "Memory leak in TLS handshake.",
		CWE:         []string{"CWE-400"},
		References:  []string{"https://example.com/advisory", "https://example.com/patch"},
		PublishedAt: "2024-03-15T00:00:00.000",
	}
	got := formatCVEDetailMarkdown(d)
	for _, want := range []string{
		"## CVE Detail (NVD authoritative)",
		"**ID**: CVE-2024-1234",
		"**CVSS**: 7.5 HIGH",
		"CVSS:3.1/AV:N",
		"**CWE**: CWE-400",
		"**Published**: 2024-03-15",
		"**Description**: Memory leak in TLS handshake.",
		"**References**:",
		"https://example.com/advisory",
		"https://example.com/patch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted markdown missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestFormatCVEDetailMarkdown_DescriptionVerbatim(t *testing.T) {
	// Vendor-provided text containing markdown punctuation appears verbatim
	// in the description line. The structural defense against fence-breaking
	// markdown / LLM-injection lives one layer up (assistant.go wraps the
	// entire prefetched block in fenceForLLM, which sizes its fence past
	// any backtick run inside). Inner fences here would only double-wrap.
	d := &CVEDetail{
		CVEID:       "CVE-2099-9999",
		Description: "Affected by [click here](javascript:alert(1)) and `rm -rf /` with # heading-like text.",
	}
	got := formatCVEDetailMarkdown(d)
	want := "**Description**: Affected by [click here](javascript:alert(1)) and `rm -rf /` with # heading-like text."
	if !strings.Contains(got, want) {
		t.Errorf("description not preserved verbatim:\n--- want substring ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestFormatCVEDetailMarkdown_Nil(t *testing.T) {
	if formatCVEDetailMarkdown(nil) != "" {
		t.Error("nil detail should produce empty string")
	}
}

func TestFormatCVEDetailMarkdown_PartialFields(t *testing.T) {
	// Only the ID + CVSS — no description, no CWE, no refs. Each optional
	// line is omitted rather than rendered empty.
	d := &CVEDetail{CVEID: "CVE-2024-X", CVSSScore: 5.0, Severity: "medium", CVSSVector: "v"}
	got := formatCVEDetailMarkdown(d)
	if strings.Contains(got, "**Description**") {
		t.Error("description line should be omitted when empty")
	}
	if strings.Contains(got, "**CWE**") {
		t.Error("CWE line should be omitted when empty")
	}
	if strings.Contains(got, "**References**") {
		t.Error("references line should be omitted when empty")
	}
	if !strings.Contains(got, "**CVSS**: 5.0 MEDIUM") {
		t.Errorf("expected CVSS line, got:\n%s", got)
	}
}

// stubCVELookup satisfies CVELookup for unit testing the prefetch path
// without an HTTP round-trip to NVD. mode controls the response: "found"
// returns a populated detail, "unknown" returns (nil, nil), "fail"
// returns an error.
type stubCVELookup struct {
	mode string
	id   string
}

func (s *stubCVELookup) LookupCVE(_ context.Context, cveID string) (*CVEDetail, error) {
	s.id = cveID
	switch s.mode {
	case "found":
		return &CVEDetail{
			CVEID: cveID, CVSSScore: 9.8, Severity: "critical",
			Description: "demo", CVSSVector: "v",
		}, nil
	case "fail":
		return nil, errors.New("nvd unreachable")
	default:
		return nil, nil
	}
}

func TestAssistantLookupCVEDetail(t *testing.T) {
	// No lookup wired → empty.
	a := &Assistant{}
	if got := a.lookupCVEDetail(context.Background(), "CVE-2024-1"); got != "" {
		t.Errorf("unwired lookup should yield empty, got %q", got)
	}

	// Wired, found → markdown block.
	stub := &stubCVELookup{mode: "found"}
	a.SetCVELookup(stub)
	got := a.lookupCVEDetail(context.Background(), "CVE-2024-1")
	if !strings.Contains(got, "## CVE Detail (NVD authoritative)") {
		t.Errorf("wired lookup with hit should yield a markdown block, got:\n%s", got)
	}
	if stub.id != "CVE-2024-1" {
		t.Errorf("lookup forwarded id=%q, want CVE-2024-1", stub.id)
	}

	// Wired, unknown CVE → empty (no block, no error noise in the prompt).
	a.SetCVELookup(&stubCVELookup{mode: "unknown"})
	if got := a.lookupCVEDetail(context.Background(), "CVE-2099-1"); got != "" {
		t.Errorf("unknown CVE should yield empty, got %q", got)
	}

	// Wired, fetch error → empty (graceful degradation).
	a.SetCVELookup(&stubCVELookup{mode: "fail"})
	if got := a.lookupCVEDetail(context.Background(), "CVE-2024-1"); got != "" {
		t.Errorf("fetch error should yield empty, got %q", got)
	}

	// Empty CVE ID → empty without invoking the stub.
	called := &stubCVELookup{mode: "found"}
	a.SetCVELookup(called)
	if got := a.lookupCVEDetail(context.Background(), ""); got != "" {
		t.Errorf("empty CVE ID should yield empty, got %q", got)
	}
	if called.id != "" {
		t.Errorf("empty CVE ID should not invoke LookupCVE, got id=%q", called.id)
	}
}
