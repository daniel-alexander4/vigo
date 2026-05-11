// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The AI-question helpers feed the per-card "Ask AI" buttons in the security
// drawer. The system prompt at server/ai/prompt.go pattern-matches on the
// `<host> · <kind>` shape — silently changing this format here would break
// the prompt-template recognition. These tests pin the format so a regression
// surfaces immediately rather than degrading to bare-word AI replies.

func TestCachedCVEAIQuestion_Full(t *testing.T) {
	got := cachedCVEAIQuestion("webby01", CachedCVE{
		ID: "CVE-2023-24538", Severity: "critical", Packages: 1,
		Title: "html/template: improper sanitization of JS template literals",
	})
	want := "webby01 · cve CVE-2023-24538 (critical) — html/template: improper sanitization of JS template literals (https://www.cve.org/CVERecord?id=CVE-2023-24538)"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestCachedCVEAIQuestion_MultiPkgNoTitle(t *testing.T) {
	got := cachedCVEAIQuestion("h", CachedCVE{ID: "CVE-2022-27664", Severity: "high", Packages: 3})
	want := "h · cve CVE-2022-27664 (high), 3 packages (https://www.cve.org/CVERecord?id=CVE-2022-27664)"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestCachedCVEAIQuestion_Bare(t *testing.T) {
	got := cachedCVEAIQuestion("h", CachedCVE{ID: "CVE-2024-1234", Severity: "medium", Packages: 1})
	want := "h · cve CVE-2024-1234 (medium) (https://www.cve.org/CVERecord?id=CVE-2024-1234)"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestTrivyAIQuestion_Format(t *testing.T) {
	got := trivyAIQuestion("webby01", TrivyVuln{
		CVEID: "CVE-2024-1234", Severity: "high",
		Package: "libssl3", Installed: "3.0.11", Fixed: "3.0.12",
		Title: "Memory leak in TLS handshake",
	})
	want := "webby01 · trivy CVE-2024-1234 (high): libssl3 3.0.11→3.0.12 — Memory leak in TLS handshake (https://www.cve.org/CVERecord?id=CVE-2024-1234)"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestTrivyAIQuestion_NoFixOrTitle(t *testing.T) {
	got := trivyAIQuestion("h", TrivyVuln{
		CVEID: "CVE-X", Severity: "low", Package: "p", Installed: "1.0",
	})
	want := "h · trivy CVE-X (low): p 1.0 (https://www.cve.org/CVERecord?id=CVE-X)"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestDebsecanAIQuestion_Format(t *testing.T) {
	got := debsecanAIQuestion("h", DebsecanVuln{CVEID: "CVE-Y", Urgency: "high", Package: "nginx"})
	want := "h · debsecan CVE-Y (high): nginx (https://security-tracker.debian.org/tracker/CVE-Y)"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestLynisWarningAIQuestion_Format(t *testing.T) {
	got := lynisWarningAIQuestion("h", LynisEntry{ID: "AUTH-9286", Text: "User accounts with empty password fields"})
	want := "h · lynis-warn AUTH-9286: User accounts with empty password fields"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestLynisSuggestionAIQuestion_Format(t *testing.T) {
	got := lynisSuggestionAIQuestion("h", LynisEntry{ID: "FIN-44567", Text: "Install asnmap for forensics"})
	want := "h · lynis-sug FIN-44567: Install asnmap for forensics"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRkhunterAIQuestion_Format(t *testing.T) {
	got := rkhunterAIQuestion("h", "/usr/bin/curl")
	want := "h · rkhunter: /usr/bin/curl flagged"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestChkrootkitAIQuestion_WithSuspect(t *testing.T) {
	got := chkrootkitAIQuestion("h", "Linux/Suckit", "/sbin/init.bak")
	want := "h · chkrootkit Linux/Suckit (suspect: /sbin/init.bak)"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestChkrootkitAIQuestion_NoSuspect(t *testing.T) {
	got := chkrootkitAIQuestion("h", "FooCheck", "")
	want := "h · chkrootkit FooCheck"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestClamavAIQuestion_Format(t *testing.T) {
	got := clamavAIQuestion("h", ClamAVFinding{Path: "/tmp/x.bin", Signature: "Unix.Trojan.Mirai-1"})
	want := "h · clamav Unix.Trojan.Mirai-1 in /tmp/x.bin"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestAideAIQuestion_Format(t *testing.T) {
	got := aideAIQuestion("h", "changed", "/etc/passwd")
	want := "h · aide changed: /etc/passwd"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestDefenderAIQuestion_Format(t *testing.T) {
	got := defenderAIQuestion("h", 3)
	want := "h · defender: 3 threats"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestScapAIQuestion_Format(t *testing.T) {
	got := scapAIQuestion("h", &SCAPDetail{Profile: "stig-rhel9", Fail: 47, HighFail: 12})
	want := `h · scap "stig-rhel9": 47 fail (12 high)`
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestScapAIQuestion_Nil(t *testing.T) {
	if scapAIQuestion("h", nil) != "" {
		t.Errorf("expected empty string for nil detail")
	}
}

func TestSecurityAIQuestion_Format(t *testing.T) {
	// Generic helper used by the cached-summary tiles. Format must remain
	// "<host> · <finding>" so the system-prompt pattern matches.
	got := securityAIQuestion("webby01", "lynis-summary: 67/100, 14 warnings")
	want := "webby01 · lynis-summary: 67/100, 14 warnings"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestDdgLink_Encodes(t *testing.T) {
	got := ddgLink("rkhunter /usr/bin/curl")
	if !strings.HasPrefix(got, "https://duckduckgo.com/?q=") {
		t.Errorf("missing ddg prefix: %q", got)
	}
	if !strings.Contains(got, "rkhunter") || strings.Contains(got, " ") {
		t.Errorf("query not URL-encoded: %q", got)
	}
}

func TestTrivySevCounts_Ranking(t *testing.T) {
	vulns := []TrivyVuln{
		{Severity: "low"}, {Severity: "high"}, {Severity: "high"},
		{Severity: "critical"}, {Severity: "medium"}, {Severity: ""},
	}
	got := trivySevCounts(vulns)
	wantOrder := []string{"critical", "high", "medium", "low", "unassigned"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got), len(wantOrder), got)
	}
	for i, b := range got {
		if b.Sev != wantOrder[i] {
			t.Errorf("bucket %d: got sev=%q, want %q", i, b.Sev, wantOrder[i])
		}
	}
	if got[1].Count != 2 {
		t.Errorf("expected 2 high, got %d", got[1].Count)
	}
}

func TestTrivySevCounts_DropsZero(t *testing.T) {
	got := trivySevCounts([]TrivyVuln{{Severity: "high"}, {Severity: "high"}})
	if len(got) != 1 || got[0].Sev != "high" || got[0].Count != 2 {
		t.Errorf("expected one high=2 bucket, got %+v", got)
	}
}

func TestDebsecanUrgencyCounts_RanksAndDrops(t *testing.T) {
	got := debsecanUrgencyCounts([]DebsecanVuln{
		{Urgency: "low"}, {Urgency: "high"}, {Urgency: "low"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(got), got)
	}
	if got[0].Sev != "high" || got[1].Sev != "low" {
		t.Errorf("ordering wrong: %+v", got)
	}
}

func TestBuildSecurityCached_UniqueLeadsAndScannerCountsFollow(t *testing.T) {
	// A Debian host: Trivy + debsecan both ran; the cross-scanner unique
	// counts are the canonical figure, the per-scanner rows are raw
	// package-match counts (debsecan's flood of Low ⇒ unique.Low ≫ trivy.Low).
	flat := map[string]string{
		"security_scan.unique_critical":     "14",
		"security_scan.unique_high":         "157",
		"security_scan.unique_medium":       "174",
		"security_scan.unique_low":          "7930",
		"security_scan.trivy.critical":      "120",
		"security_scan.trivy.high":          "1553",
		"security_scan.trivy.medium":        "1804",
		"security_scan.trivy.low":           "436",
		"security_scan.trivy.total":         "3913",
		"security_scan.trivy.last_scan":     "2026-05-10T13:01:54Z",
		"security_scan.debsecan.high":       "0",
		"security_scan.debsecan.medium":     "12",
		"security_scan.debsecan.low":        "7500",
		"security_scan.debsecan.unassigned": "1",
		"security_scan.debsecan.total":      "7513",
		"security_scan.lynis.score":         "78",
		// Two CVEs rode the wire; unique total (8275) far exceeds it.
		"security_scan.cves.0.id":       "CVE-2026-1",
		"security_scan.cves.0.severity": "critical",
		"security_scan.cves.1.id":       "CVE-2026-2",
		"security_scan.cves.1.severity": "high",
	}
	got := buildSecurityCached(flat)

	if got.Unique == nil {
		t.Fatal("Unique should be populated")
	}
	if got.Unique.Critical != 14 || got.Unique.Low != 7930 {
		t.Errorf("Unique = %+v, want Critical=14 Low=7930", *got.Unique)
	}
	if got.Unique.Total() != 14+157+174+7930 {
		t.Errorf("Unique.Total() = %d, want %d", got.Unique.Total(), 14+157+174+7930)
	}
	if got.Trivy == nil || got.Trivy.Critical != 120 || got.Trivy.Total != 3913 ||
		got.Trivy.LastScan != "2026-05-10T13:01:54Z" {
		t.Errorf("Trivy = %+v, want Critical=120 Total=3913", got.Trivy)
	}
	if got.Debsecan == nil || got.Debsecan.Low != 7500 || got.Debsecan.Unassigned != 1 || got.Debsecan.Total != 7513 {
		t.Errorf("Debsecan = %+v, want Low=7500 Unassigned=1 Total=7513", got.Debsecan)
	}
	if got.Lynis == nil || got.Lynis.Score != 78 {
		t.Errorf("Lynis = %+v, want Score=78", got.Lynis)
	}
	if !got.CVEsTruncated {
		t.Error("CVEsTruncated should be true (2 listed CVEs vs unique total 8275)")
	}
}

func TestBuildSecurityCached_NoTruncationWhenListCoversUnique(t *testing.T) {
	flat := map[string]string{
		"security_scan.unique_critical": "1",
		"security_scan.unique_high":     "1",
		"security_scan.cves.0.id":       "CVE-A",
		"security_scan.cves.0.severity": "critical",
		"security_scan.cves.1.id":       "CVE-B",
		"security_scan.cves.1.severity": "high",
	}
	got := buildSecurityCached(flat)
	if got.Unique == nil || got.Unique.Total() != 2 {
		t.Fatalf("Unique.Total() = %v, want 2", got.Unique)
	}
	if got.CVEsTruncated {
		t.Error("CVEsTruncated should be false when the list covers the unique count")
	}
	if got.Trivy != nil || got.Debsecan != nil {
		t.Error("no Trivy/Debsecan cards expected when those keys are absent")
	}
}

func TestBuildSecurityCached_NoScanData(t *testing.T) {
	got := buildSecurityCached(map[string]string{})
	if got.Unique != nil || got.Trivy != nil || got.Debsecan != nil || got.CVEsTruncated || len(got.CVEs) != 0 {
		t.Errorf("empty traits should yield an empty summary, got %+v", got)
	}
}

func TestBuildSecurityCached_RootkitScannersAreSeparate(t *testing.T) {
	// Only chkrootkit reported, and it's not clean.
	got := buildSecurityCached(map[string]string{
		"security_scan.chkrootkit.clean":    "false",
		"security_scan.chkrootkit.infected": "2",
	})
	if got.Chkrootkit == nil || got.Chkrootkit.Clean || got.Chkrootkit.Issues != 2 {
		t.Errorf("Chkrootkit = %+v, want {Clean:false Issues:2}", got.Chkrootkit)
	}
	if got.Rkhunter != nil {
		t.Errorf("Rkhunter should be nil when rkhunter didn't report, got %+v", got.Rkhunter)
	}

	// Only rkhunter reported, clean (absent value treated as clean).
	got = buildSecurityCached(map[string]string{
		"security_scan.rkhunter.clean":    "true",
		"security_scan.rkhunter.warnings": "0",
	})
	if got.Rkhunter == nil || !got.Rkhunter.Clean || got.Rkhunter.Issues != 0 {
		t.Errorf("Rkhunter = %+v, want {Clean:true Issues:0}", got.Rkhunter)
	}
	if got.Chkrootkit != nil {
		t.Errorf("Chkrootkit should be nil when chkrootkit didn't report, got %+v", got.Chkrootkit)
	}
}

func sectionSlugs(s []SecuritySection) []string {
	out := make([]string, len(s))
	for i, sec := range s {
		out[i] = sec.Slug
	}
	return out
}

func TestDrawerSections_CachedThenLive(t *testing.T) {
	cached := SecurityDrawerCached{
		Unique: &UniqueCVECounts{Critical: 1},
		Trivy:  &TrivyCached{},
		Lynis:  &LynisCached{},
		AIDE:   &AIDECached{},
	}
	// Before the live fetch: scanners come from cached data; CVEs leads,
	// Certs trails.
	got := sectionSlugs(drawerSections(cached, nil))
	if want := []string{"cves", "trivy", "lynis", "aide", "certs"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("not-live sections = %v, want %v", got, want)
	}
	// After the fetch: scanner tabs come from the live detail (here Trivy +
	// Defender), not the cached set; CVEs (a check-in-trait rollup) and
	// Certs still bracket them.
	detail := &SecurityScanDetail{Trivy: &TrivyDetail{}, Defender: &DefenderDetail{}}
	got = sectionSlugs(drawerSections(cached, detail))
	if want := []string{"cves", "trivy", "defender", "certs"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("live sections = %v, want %v", got, want)
	}
	// No cached CVE data and no live detail: just the always-present Certs tab.
	got = sectionSlugs(drawerSections(SecurityDrawerCached{}, nil))
	if want := []string{"certs"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("empty sections = %v, want %v", got, want)
	}
}

func TestSecurityDrawerFragment_RendersBothStates(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	data := SecurityDrawerData{
		EnvoyID:  "00000000-0000-0000-0000-000000000001",
		Hostname: "danlap",
		Timeout:  90,
		Cached: SecurityDrawerCached{
			Unique:     &UniqueCVECounts{Critical: 2, High: 5},
			Trivy:      &TrivyCached{Critical: 1, Total: 1},
			Lynis:      &LynisCached{Score: 78, Warnings: 3, Suggestions: 9},
			Chkrootkit: &RootkitScannerCached{Clean: true},
			AIDE:       &AIDECached{Clean: false, Added: 1},
			CVEs:       []CachedCVE{{ID: "CVE-2024-0001", Severity: "critical", Packages: 1}},
		},
		Certs: []CertDetail{{CN: "x.example", Issuer: "Let's Encrypt", NotAfter: "2026-06-01", DaysRemaining: 10}},
	}

	// Not-live: cached summaries + the "Fetch Live Detail" affordance; no
	// Defender tab (no cached Defender summary).
	data.Sections = drawerSections(data.Cached, nil)
	rec := httptest.NewRecorder()
	r.RenderFragment(rec, "security", "security-drawer-fragment", data)
	body := rec.Body.String()
	for _, want := range []string{
		`id="sec-pane-cves"`, `id="sec-pane-trivy"`, `id="sec-pane-lynis"`,
		`id="sec-pane-chkrootkit"`, `id="sec-pane-aide"`, `id="sec-pane-certs"`,
		"Fetch Live Detail", "Lynis Hardening", "CVE-2024-0001",
		"Fetch Live Detail above for individual findings",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("not-live: rendered drawer missing %q", want)
		}
	}
	if strings.Contains(body, `id="sec-pane-defender"`) {
		t.Error("not-live: Defender tab should be absent (no cached Defender data)")
	}

	// Live: the per-finding cards replace the summaries; scanner tabs now
	// come from Detail (Trivy + Lynis + Defender); the chkrootkit/aide tabs
	// drop off (no live detail for them).
	data.Live = true
	data.Detail = &SecurityScanDetail{
		Trivy:    &TrivyDetail{Vulnerabilities: []TrivyVuln{{CVEID: "CVE-2024-9999", Severity: "high", Package: "openssl", Installed: "1.0.0"}}, LastScan: "now"},
		Lynis:    &LynisDetail{Score: 78, Warnings: []LynisEntry{{ID: "AUTH-0001", Text: "weak setting"}}, LastScan: "now"},
		Defender: &DefenderDetail{Enabled: true, RealtimeProtection: true, LastScan: "now"},
	}
	data.Sections = drawerSections(data.Cached, data.Detail)
	rec = httptest.NewRecorder()
	r.RenderFragment(rec, "security", "security-drawer-fragment", data)
	body = rec.Body.String()
	for _, want := range []string{
		`id="sec-pane-cves"`, `id="sec-pane-trivy"`, `id="sec-pane-lynis"`,
		`id="sec-pane-defender"`, `id="sec-pane-certs"`,
		"Refresh Live Detail", "CVE-2024-9999", "AUTH-0001",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live: rendered drawer missing %q", want)
		}
	}
	if strings.Contains(body, "Fetch Live Detail above for individual findings") {
		t.Error("live: the cached-summary hint should not appear once detail is fetched")
	}
}
