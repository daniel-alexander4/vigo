// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import "testing"

func TestRunningBinariesForCVE_Matches(t *testing.T) {
	traitMap := map[string]string{
		// Row 0 matches the target CVE.
		"cve_running_correlation.running_cves.0.cve_id":          "CVE-2024-1234",
		"cve_running_correlation.running_cves.0.severity":        "critical",
		"cve_running_correlation.running_cves.0.package":         "openssl",
		"cve_running_correlation.running_cves.0.package_version": "3.0.2",
		"cve_running_correlation.running_cves.0.binary_path":     "/usr/bin/openssl",
		"cve_running_correlation.running_cves.0.pid_count":       "2",
		"cve_running_correlation.running_cves.0.deleted":         "false",
		// Row 1 matches the target CVE on a different binary.
		"cve_running_correlation.running_cves.1.cve_id":      "CVE-2024-1234",
		"cve_running_correlation.running_cves.1.severity":    "critical",
		"cve_running_correlation.running_cves.1.package":     "openssl",
		"cve_running_correlation.running_cves.1.binary_path": "/usr/sbin/sshd",
		"cve_running_correlation.running_cves.1.pid_count":   "1",
		"cve_running_correlation.running_cves.1.deleted":     "true",
		// Row 2 is a different CVE — must not be returned.
		"cve_running_correlation.running_cves.2.cve_id":      "CVE-2024-9999",
		"cve_running_correlation.running_cves.2.severity":    "medium",
		"cve_running_correlation.running_cves.2.binary_path": "/usr/bin/curl",
		"cve_running_correlation.running_cves.2.pid_count":   "1",
		"cve_running_correlation.running_cves.2.deleted":     "false",
	}

	bins, severity := runningBinariesForCVE(traitMap, "CVE-2024-1234")
	if len(bins) != 2 {
		t.Fatalf("expected 2 matching rows, got %d: %+v", len(bins), bins)
	}
	if severity != "critical" {
		t.Errorf("expected severity 'critical', got %q", severity)
	}
	// Sorted by binary_path asc.
	if bins[0].BinaryPath != "/usr/bin/openssl" || bins[1].BinaryPath != "/usr/sbin/sshd" {
		t.Errorf("rows out of order: %+v", bins)
	}
	if bins[0].PidCount != 2 {
		t.Errorf("expected pid_count=2 on openssl, got %d", bins[0].PidCount)
	}
	if !bins[1].Deleted {
		t.Errorf("expected deleted=true on sshd, got %v", bins[1].Deleted)
	}
}

func TestRunningBinariesForCVE_CaseInsensitive(t *testing.T) {
	traitMap := map[string]string{
		"cve_running_correlation.running_cves.0.cve_id":      "cve-2024-1234",
		"cve_running_correlation.running_cves.0.severity":    "high",
		"cve_running_correlation.running_cves.0.binary_path": "/usr/bin/foo",
		"cve_running_correlation.running_cves.0.pid_count":   "1",
	}
	bins, sev := runningBinariesForCVE(traitMap, "CVE-2024-1234")
	if len(bins) != 1 || sev != "high" {
		t.Errorf("case-insensitive match failed: bins=%+v sev=%q", bins, sev)
	}
}

func TestRunningBinariesForCVE_NoMatch(t *testing.T) {
	traitMap := map[string]string{
		"cve_running_correlation.running_cves.0.cve_id":      "CVE-2024-9999",
		"cve_running_correlation.running_cves.0.binary_path": "/usr/bin/foo",
	}
	bins, sev := runningBinariesForCVE(traitMap, "CVE-2024-1234")
	if len(bins) != 0 {
		t.Errorf("expected no matches, got %+v", bins)
	}
	if sev != "" {
		t.Errorf("expected empty severity when no match, got %q", sev)
	}
}

func TestRunningBinariesForCVE_EmptyTraitMap(t *testing.T) {
	bins, sev := runningBinariesForCVE(map[string]string{}, "CVE-2024-1234")
	if len(bins) != 0 || sev != "" {
		t.Errorf("expected empty result for empty trait map, got bins=%+v sev=%q", bins, sev)
	}
}
