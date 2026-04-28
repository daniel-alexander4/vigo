// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import "testing"

// TestFrameworkCatalogCounts pins the per-framework control catalog
// size against the targets agreed for the 0.32.x catalog-completion
// work. Drift in either direction is caught here so accidental
// truncation or duplicate insertion is loud.
//
// The targets are floors and ceilings rather than exact numbers — the
// canonical specifications occasionally renumber sub-controls between
// minor revisions, and we don't want to fail on every spec re-print.
// If a framework's count moves outside its band, update the band here
// and update docs/compliance/_index.md to match.
func TestFrameworkCatalogCounts(t *testing.T) {
	h := &ReportHandler{}
	cases := []struct {
		name     string
		findings []ControlFinding
		minOK    int
		maxOK    int
	}{
		// Public-domain frameworks — full enumeration possible.
		{"HIPAA Security Rule", h.buildHIPAAFindings(), 60, 80},
		{"NIST 800-53 Rev 5 Moderate (incl. PM family)", h.buildNIST80053Findings(), 280, 340},
		{"GDPR", h.buildGDPRFindings(), 35, 60},
		// Licensed frameworks — IDs + short titles only.
		{"SOC 2 TSC", h.buildSOC2Findings(), 50, 80},
		{"PCI DSS v4.0", h.buildPCIDSSFindings(), 220, 300},
		{"ISO 27001:2022 Annex A", h.buildISO27001Findings(), 88, 100},
		{"CIS Ubuntu 22.04 L1+L2", h.buildCISUbuntuFindings(), 220, 320},
		{"CIS RHEL 8/9 L1+L2", h.buildCISRHELFindings(), 180, 290},
		{"CIS Windows Server 2022 L1+MS", h.buildCISWindowsFindings(), 320, 420},
	}
	for _, tc := range cases {
		got := len(tc.findings)
		if got < tc.minOK || got > tc.maxOK {
			t.Errorf("%s: catalog has %d controls, want between %d and %d", tc.name, got, tc.minOK, tc.maxOK)
		}
	}
}

// TestFrameworkCatalogsAreUniqueAndStatusInitialized verifies every
// catalog entry has a unique ControlID within its framework and a
// non-empty default Status. Empty Status would cause partition logic
// to silently miscount; duplicate IDs would cause module-tag coverage
// to over-count.
func TestFrameworkCatalogsAreUniqueAndStatusInitialized(t *testing.T) {
	h := &ReportHandler{}
	cases := []struct {
		name     string
		findings []ControlFinding
	}{
		{"HIPAA", h.buildHIPAAFindings()},
		{"HITRUST", h.buildHITRUSTFindings()},
		{"SOC 2", h.buildSOC2Findings()},
		{"PCI DSS", h.buildPCIDSSFindings()},
		{"NIST 800-53", h.buildNIST80053Findings()},
		{"ISO 27001", h.buildISO27001Findings()},
		{"GDPR", h.buildGDPRFindings()},
		{"NERC CIP", h.buildNERCCIPFindings()},
		{"Cyber Essentials+", h.buildCyberEssentialsPlusFindings()},
		{"IEC 62443", h.buildIEC62443Findings()},
		{"SOX", h.buildSOXFindings()},
		{"FINRA", h.buildFINRAFindings()},
		{"MiFID II", h.buildMiFID2Findings()},
		{"CCSS", h.buildCCSSFindings()},
		{"FDA 21 CFR Part 11", h.buildFDA21CFR11Findings()},
		{"Gaming", h.buildGamingFindings()},
		{"CIS Windows", h.buildCISWindowsFindings()},
		{"CIS Ubuntu", h.buildCISUbuntuFindings()},
		{"CIS RHEL", h.buildCISRHELFindings()},
		{"ABA Cyber", h.buildABACyberFindings()},
		{"NY DFS Cyber", h.buildNYDFSCyberFindings()},
	}
	for _, tc := range cases {
		seen := make(map[string]bool, len(tc.findings))
		for _, f := range tc.findings {
			if f.ControlID == "" {
				t.Errorf("%s: empty ControlID found", tc.name)
				continue
			}
			if seen[f.ControlID] {
				t.Errorf("%s: duplicate ControlID %q", tc.name, f.ControlID)
			}
			seen[f.ControlID] = true
			if f.Status == "" {
				t.Errorf("%s: %q has empty Status (catalogControl helper bypassed?)", tc.name, f.ControlID)
			}
			if f.Title == "" {
				t.Errorf("%s: %q has empty Title", tc.name, f.ControlID)
			}
		}
	}
}
