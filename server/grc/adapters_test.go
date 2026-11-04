// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grc

import (
	"strings"
	"testing"
	"time"
)

// TestAdapters_WaivedIsNotFailed: every adapter used to map anything but
// satisfied/partial to the platform's failing status, so a waived control (an
// accepted exception) was pushed as a failure. Each adapter must report it as
// the platform's waived/not-applicable state, or as passing with a note.
func TestAdapters_WaivedIsNotFailed(t *testing.T) {
	p := &EvidencePayload{
		Timestamp: time.Unix(0, 0).UTC(),
		Frameworks: []FrameworkEvidence{{
			ID: "soc2", Name: "SOC 2",
			Controls: []ControlStatus{{ID: "CC1.1", Title: "t", Status: "waived", Evidence: "ev"}},
		}},
	}
	cases := []struct {
		adapter    string
		wantStatus string
		wantNote   bool
	}{
		{"vanta", `"result":"PASS"`, true},
		{"laika", `"result":"PASS"`, true},
		{"drata", `"status":"not_applicable"`, false},
		{"secureframe", `"status":"pass"`, true},
		{"thoropass", `"status":"pass"`, true},
		{"servicenow", `"status":"compliant"`, true},
		{"generic", `"status":"waived"`, false},
	}
	for _, c := range cases {
		out, err := GetAdapter(c.adapter).Format(p)
		if err != nil {
			t.Fatalf("%s: %v", c.adapter, err)
		}
		body := string(out)
		if !strings.Contains(body, c.wantStatus) {
			t.Errorf("%s: waived control body %s, want %s", c.adapter, body, c.wantStatus)
		}
		if c.wantNote && !strings.Contains(body, "Waived (accepted exception") {
			t.Errorf("%s: waived control reported passing without the waiver note: %s", c.adapter, body)
		}
	}
}
