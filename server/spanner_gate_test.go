package main

import (
	"testing"

	"git.alexander4.io/vigo/server/config"
)

type fakeLicenseChecker struct{ commercial bool }

func (f fakeLicenseChecker) IsCommercialLicense() bool { return f.commercial }

// TestSpannerAllowed covers the spanner-mode startup gate directly. NewServer
// is too heavy to invoke in a unit test (config/TLS/DB harness), so the gate
// was extracted into spannerAllowed; this exercises the mode × license
// quadrant that the predicate-only TestIsCommercialLicense never reached.
func TestSpannerAllowed(t *testing.T) {
	cases := []struct {
		name       string
		mode       config.SpannerMode
		commercial bool
		wantErr    bool
	}{
		{"spanner-community-rejected", config.ModeSpanner, false, true},
		{"spanner-commercial-allowed", config.ModeSpanner, true, false},
		{"standalone-community-allowed", config.ModeStandalone, false, false},
		{"standalone-commercial-allowed", config.ModeStandalone, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := spannerAllowed(tc.mode, fakeLicenseChecker{commercial: tc.commercial})
			if tc.wantErr && err == nil {
				t.Fatalf("spannerAllowed(%q, commercial=%v) = nil, want error", tc.mode, tc.commercial)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("spannerAllowed(%q, commercial=%v) = %v, want nil", tc.mode, tc.commercial, err)
			}
		})
	}
}
