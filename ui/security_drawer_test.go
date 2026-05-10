// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
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
