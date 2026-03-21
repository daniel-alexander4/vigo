// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// openExecutiveTestDB creates an in-memory SQLite database with tables needed for executive report tests.
func openExecutiveTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE audit_entries (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			prev_hash   TEXT NOT NULL DEFAULT '',
			hash        TEXT NOT NULL,
			timestamp   DATETIME NOT NULL DEFAULT (datetime('now')),
			event_type  TEXT NOT NULL,
			actor       TEXT NOT NULL DEFAULT '',
			summary     TEXT NOT NULL DEFAULT '',
			payload     TEXT NOT NULL DEFAULT '{}'
		);
		CREATE TABLE compliance_history (
			id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			recorded_at    TEXT NOT NULL UNIQUE,
			total          INTEGER NOT NULL DEFAULT 0,
			compliant      INTEGER NOT NULL DEFAULT 0,
			recurring_drift INTEGER NOT NULL DEFAULT 0,
			errors         INTEGER NOT NULL DEFAULT 0,
			lost_contact   INTEGER NOT NULL DEFAULT 0,
			no_data        INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

func newExecutiveTestHandler(t *testing.T, fi *fleet.FleetIndex, queries *db.Queries) (*ReportHandler, *http.ServeMux) {
	t.Helper()
	h := &ReportHandler{
		queries:    queries,
		fleetIndex: fi,
		version:    "1.0.0-test",
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

func TestExecutiveReport_JSONContent(t *testing.T) {
	_, queries := openExecutiveTestDB(t)
	fi := fleet.NewFleetIndex()

	fi.AddEnvoy(fleet.EnvoyState{
		ID: "e1", Hostname: "web1.example.com",
		ComplianceStatus: "compliant", LastSeen: time.Now(),
	}, nil)

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected application/json, got %s", ct)
	}

	var report ExecutiveReport
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if report.Score.Percent != 100 {
		t.Errorf("score.percent = %v, want 100", report.Score.Percent)
	}
	if report.Score.Rating != "Excellent" {
		t.Errorf("score.rating = %q, want Excellent", report.Score.Rating)
	}
	if report.Fleet.Total != 1 {
		t.Errorf("fleet.total = %d, want 1", report.Fleet.Total)
	}
	if report.Fleet.FullyCompliant != 1 {
		t.Errorf("fleet.fully_compliant = %d, want 1", report.Fleet.FullyCompliant)
	}
	if len(report.Frameworks) != 10 {
		t.Errorf("frameworks count = %d, want 10", len(report.Frameworks))
	}
}

func TestExecutiveReport_HTMLContent(t *testing.T) {
	_, queries := openExecutiveTestDB(t)
	fi := fleet.NewFleetIndex()

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive.html", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html, got %s", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Executive Compliance Summary",
		"Framework Coverage",
		"@media print",
		"audit-banner",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

func TestExecutiveReport_DownloadHeader(t *testing.T) {
	_, queries := openExecutiveTestDB(t)
	fi := fleet.NewFleetIndex()

	_, mux := newExecutiveTestHandler(t, fi, queries)

	// JSON download.
	req := httptest.NewRequest("GET", "/api/v1/report/executive?download=1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "vigo-executive-") {
		t.Errorf("JSON Content-Disposition = %q, want attachment with vigo-executive-", cd)
	}

	// HTML download.
	req = httptest.NewRequest("GET", "/api/v1/report/executive.html?download=1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	cd = rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".html") {
		t.Errorf("HTML Content-Disposition = %q, want attachment with .html", cd)
	}
}

func TestExecutiveReport_EmptyFleet(t *testing.T) {
	_, queries := openExecutiveTestDB(t)
	fi := fleet.NewFleetIndex()

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var report ExecutiveReport
	json.NewDecoder(rec.Body).Decode(&report)

	if report.Score.Percent != 0 {
		t.Errorf("score.percent = %v, want 0 for empty fleet", report.Score.Percent)
	}
	if report.Score.Rating != "Critical" {
		t.Errorf("score.rating = %q, want Critical for 0%%", report.Score.Rating)
	}
	if report.Fleet.Total != 0 {
		t.Errorf("fleet.total = %d, want 0", report.Fleet.Total)
	}
}

func TestExecutiveReport_TopIssuesOrdering(t *testing.T) {
	_, queries := openExecutiveTestDB(t)
	fi := fleet.NewFleetIndex()

	fi.AddEnvoy(fleet.EnvoyState{ID: "e1", Hostname: "ok.example.com", ComplianceStatus: "compliant", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e2", Hostname: "err.example.com", ComplianceStatus: "errors", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e3", Hostname: "stale.example.com", ComplianceStatus: "lost contact", LastSeen: time.Now().Add(-24 * time.Hour)}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e4", Hostname: "drift.example.com", ComplianceStatus: "recurring drift", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e5", Hostname: "new.example.com", ComplianceStatus: "no data", LastSeen: time.Now()}, nil)

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var report ExecutiveReport
	json.NewDecoder(rec.Body).Decode(&report)

	if len(report.TopIssues) == 0 {
		t.Fatal("expected top issues, got none")
	}

	// First issue should be critical (audit chain broken or errors).
	if report.TopIssues[0].Severity != "critical" {
		t.Errorf("first issue severity = %q, want critical", report.TopIssues[0].Severity)
	}

	// Issues must be sorted: all critical before warning before info.
	prevRank := 0
	rankMap := map[string]int{"critical": 0, "warning": 1, "info": 2}
	for i, issue := range report.TopIssues {
		r := rankMap[issue.Severity]
		if r < prevRank {
			t.Errorf("issue[%d] severity %q appears after higher rank", i, issue.Severity)
		}
		prevRank = r
	}

	// Max 10.
	if len(report.TopIssues) > 10 {
		t.Errorf("top issues count = %d, want <= 10", len(report.TopIssues))
	}
}

func TestExecutiveReport_TrendOrdering(t *testing.T) {
	sqlDB, queries := openExecutiveTestDB(t)
	fi := fleet.NewFleetIndex()

	// Insert compliance history in chronological order — DB returns DESC.
	dates := []string{"2026-03-01", "2026-03-02", "2026-03-03"}
	for _, d := range dates {
		_, err := sqlDB.Exec(
			`INSERT INTO compliance_history (recorded_at, total, compliant, recurring_drift, errors, lost_contact, no_data) VALUES (?, 10, 8, 1, 0, 1, 0)`,
			d,
		)
		if err != nil {
			t.Fatalf("insert history: %v", err)
		}
	}

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var report ExecutiveReport
	json.NewDecoder(rec.Body).Decode(&report)

	if len(report.Trend) != 3 {
		t.Fatalf("trend length = %d, want 3", len(report.Trend))
	}

	// Should be chronological (ascending).
	if report.Trend[0].Date != "2026-03-01" {
		t.Errorf("trend[0].date = %q, want 2026-03-01", report.Trend[0].Date)
	}
	if report.Trend[2].Date != "2026-03-03" {
		t.Errorf("trend[2].date = %q, want 2026-03-03", report.Trend[2].Date)
	}

	// Each day: 8/10 = 80%.
	if report.Trend[0].Percent != 80 {
		t.Errorf("trend[0].percent = %v, want 80", report.Trend[0].Percent)
	}
}
