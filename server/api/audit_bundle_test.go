// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// TestParseAuditBundlePeriod pins the accepted formats and rejects.
// Auditors need stable URL semantics so saved query strings keep
// working across releases.
func TestParseAuditBundlePeriod(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 365 * 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30", 30 * 24 * time.Hour, false}, // bare integer = days
		{"12mo", 12 * 30 * 24 * time.Hour, false},
		{"3y", 3 * 365 * 24 * time.Hour, false},
		{"abc", 0, true},
		{"-7d", 0, true},
		{"0d", 0, true},
		{"7w", 0, true}, // unsupported unit
	}
	for _, tc := range cases {
		got, err := parseAuditBundlePeriod(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseAuditBundlePeriod(%q) err = %v, wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseAuditBundlePeriod(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestSanitizeBundlePath proves that operator-supplied filenames can't
// climb out of the bundle's docs/ directory or land at unsafe paths.
func TestSanitizeBundlePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"baa.pdf", "baa.pdf"},
		{"../../etc/passwd", "passwd"},
		{"a/b/c.pdf", "c.pdf"},
		{"a\\b\\c.pdf", "a_b_c.pdf"}, // Linux filepath.Base treats backslashes as part of name; we sanitize them to underscores
		{"", "unnamed"},
		{".", "unnamed"},
		{"..", "unnamed"},
	}
	for _, tc := range cases {
		got := sanitizeBundlePath(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeBundlePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestWriteBundleEntry_HashMatchesContents verifies that the SHA-256
// captured during write matches a fresh hash over the same bytes.
// This is the contract auditors rely on: extract the zip, sha256sum
// each path, compare to manifest.json.
func TestWriteBundleEntry_HashMatchesContents(t *testing.T) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	payload := []byte("hello bundle world")
	got, err := writeBundleEntry(zw, "x.txt", payload)
	if err != nil {
		t.Fatalf("writeBundleEntry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}
	hash := sha256.Sum256(payload)
	want := hex.EncodeToString(hash[:])
	if got != want {
		t.Errorf("hash drift: got %q want %q", got, want)
	}

	// Read the entry back from the zip and confirm contents survive
	// round-trip.
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "x.txt" {
		t.Fatalf("unexpected zip layout: %+v", zr.File)
	}
	rc, _ := zr.File[0].Open()
	defer rc.Close()
	got2, _ := io.ReadAll(rc)
	if !bytes.Equal(got2, payload) {
		t.Errorf("contents drift: got %q want %q", got2, payload)
	}
}

// TestBuildEvidenceJSON_GroupsDocsByControl pins that the evidence
// blob groups attached docs under the right control IDs and respects
// the includeDocs flag.
func TestBuildEvidenceJSON_GroupsDocsByControl(t *testing.T) {
	r := &ComplianceReport{
		Title:       "Test",
		Slug:        "hipaa",
		GeneratedAt: time.Now().Format(time.RFC3339),
		Version:     "test",
		Controls: []ControlFinding{
			{ControlID: "164.308(b)(1)", Title: "BAA", Scope: ScopeDocumented},
			{ControlID: "164.312(a)(1)", Title: "Access Control", Scope: ScopeEnforced},
		},
		Documents: []ReportDocAttachment{
			{ControlID: "164.308(b)(1)", DocID: "abc", Filename: "baa.pdf", SHA256: "deadbeef"},
		},
	}
	out := buildEvidenceJSON(r, true)
	data, _ := json.Marshal(out)
	if !strings.Contains(string(data), `"control_id":"164.308(b)(1)"`) {
		t.Errorf("missing control id in evidence: %s", data)
	}
	if !strings.Contains(string(data), `"doc_id":"abc"`) {
		t.Errorf("doc not associated with control: %s", data)
	}

	// includeDocs=false strips Documents.
	out2 := buildEvidenceJSON(r, false)
	data2, _ := json.Marshal(out2)
	if strings.Contains(string(data2), `"doc_id":"abc"`) {
		t.Errorf("docs leaked through includeDocs=false: %s", data2)
	}
}
