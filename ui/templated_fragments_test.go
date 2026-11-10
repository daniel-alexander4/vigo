// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/db"
	_ "github.com/mattn/go-sqlite3"
)

type stubCVESearcher struct{ hosts []CVEMatch }

func (s stubCVESearcher) SearchCVE(string, bool) (int, []CVEMatch) { return len(s.hosts), s.hosts }

// TestCVESearchFragment_Templated: the CVE search result renders through the
// security page's cve-search-fragment template — envoy-reported values are
// escaped by html/template, the running-binaries cell lists each binary with
// its (deleted) tag and pid multiplier, the security headers are set, and the
// empty / no-match branches render their messages.
func TestCVESearchFragment_Templated(t *testing.T) {
	h := newTestHandler(t)
	h.cveSearcher = stubCVESearcher{hosts: []CVEMatch{{
		Hostname: `<img src=x onerror=alert(1)>`, Package: "openssl", Installed: "3.0.1",
		Severity: "CRITICAL", Scanner: "trivy",
		RunningBinaries: []CVERunningBinaryRef{
			{BinaryPath: "/usr/sbin/nginx", PidCount: 3, Deleted: true},
			{BinaryPath: "/usr/bin/curl", PidCount: 1},
		},
	}}}

	rec := httptest.NewRecorder()
	h.CVESearchFragment(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/cve-search?cve=cve-2024-1", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"CVE-2024-1", "1 host(s)", "&lt;img src=x", `class="text-danger"`, "/usr/sbin/nginx<",
		"(deleted)", "×3", "<br>", `<td>-</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("CVE fragment missing %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<img src=x") {
		t.Error("hostname rendered unescaped")
	}
	if strings.Contains(body, "×1") {
		t.Error("pid multiplier shown for a single pid")
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("fragment missing security headers (not rendered via RenderFragment)")
	}

	rec = httptest.NewRecorder()
	h.CVESearchFragment(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/cve-search", nil))
	if !strings.Contains(rec.Body.String(), "Enter a CVE ID") {
		t.Errorf("empty query message missing; body:\n%s", rec.Body.String())
	}
	h.cveSearcher = stubCVESearcher{}
	rec = httptest.NewRecorder()
	h.CVESearchFragment(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/cve-search?cve=CVE-1&running_only=true", nil))
	if !strings.Contains(rec.Body.String(), "No hosts have running binaries affected by CVE-1") {
		t.Errorf("no-match running-only message missing; body:\n%s", rec.Body.String())
	}
}

// TestEnvoySessionsFragment_Templated: the Scrier session history renders
// through envoy-detail's envoy-sessions-fragment, escaping the username and
// mapping status to its badge modifier.
func TestEnvoySessionsFragment_Templated(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", "file:sesstest?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`
		CREATE TABLE scrier_sessions (
			id TEXT PRIMARY KEY, envoy_id TEXT NOT NULL, user_id TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '', protocol TEXT NOT NULL DEFAULT '',
			target_port INTEGER NOT NULL DEFAULT 0, tunnel_id TEXT NOT NULL DEFAULT '',
			started_at DATETIME NOT NULL, ended_at DATETIME, duration_ms INTEGER,
			status TEXT NOT NULL DEFAULT '', recording_path TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '');
		INSERT INTO scrier_sessions(id,envoy_id,username,protocol,target_port,started_at,duration_ms,status)
		VALUES ('s1','e1','<b>mallory</b>','ssh',22,'2026-10-01 10:00:00',65000,'closed');`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	h := newTestHandler(t)
	h.queries = db.New(sqlDB)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-sessions/e1", nil)
	req.SetPathValue("id", "e1")
	rec := httptest.NewRecorder()
	h.EnvoyScrierSessionsFragment(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"&lt;b&gt;mallory", "ssh", "<td>22</td>", "1m5s", "badge--muted"} {
		if !strings.Contains(body, want) {
			t.Errorf("sessions fragment missing %q; body:\n%s", want, body)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-sessions/none", nil)
	req.SetPathValue("id", "none")
	rec = httptest.NewRecorder()
	h.EnvoyScrierSessionsFragment(rec, req)
	if !strings.Contains(rec.Body.String(), "No remote access sessions recorded") {
		t.Errorf("empty message missing; body:\n%s", rec.Body.String())
	}
}
