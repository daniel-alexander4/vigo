// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

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

// TestExecutiveReport_RBAC proves the report routes require compliance.read.
// Before this gate they registered with a bare mux.HandleFunc — any
// authenticated principal, including a future narrow token, could read the
// report bodies and their embedded audit-chain status. A principal lacking
// compliance.read now gets 403; one holding it (admin) gets 200.
func TestExecutiveReport_RBAC(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	fi := fleet.NewFleetIndex()
	_, mux := newExecutiveTestHandler(t, fi, queries)

	// viewerReq carries only fleet.read (no compliance.read) → refused.
	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, viewerReq(req))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no compliance.read: expected 403, got %d", rec.Code)
	}

	// Unauthenticated (no user in context) → 401.
	req = httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: expected 401, got %d", rec.Code)
	}

	// admin holds compliance.read → allowed.
	req = httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestExecutiveReport_JSONContent(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	fi := fleet.NewFleetIndex()

	fi.AddEnvoy(fleet.EnvoyState{
		ID: "e1", Hostname: "web1.example.com",
		ConvergenceStatus: "converged", LastSeen: time.Now(),
	}, nil)

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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

	// Score comes from averaging framework coverage percentages. This test
	// has no configStore, so buildFrameworkCoverage returns nil and the
	// compliance percent stays at 0 — which maps to the "Critical" rating.
	// This is the honest reflection of "no compliance data available." For
	// the happy-path percent=100 assertion, set up a configStore with at
	// least one framework-tagged module and a converged envoy (separate test).
	if report.Score.Percent != 0 {
		t.Errorf("score.percent = %v, want 0 (no frameworks loaded)", report.Score.Percent)
	}
	if report.Score.Rating != "Critical" {
		t.Errorf("score.rating = %q, want Critical (0%% → Critical)", report.Score.Rating)
	}
	if report.Fleet.Total != 1 {
		t.Errorf("fleet.total = %d, want 1", report.Fleet.Total)
	}
	if report.Fleet.FullyConverged != 1 {
		t.Errorf("fleet.fully_compliant = %d, want 1", report.Fleet.FullyConverged)
	}
	// Framework count depends on compliance modules loaded in configStore.
	// With no configStore, frameworks are empty (coverage-based scoring).
	if len(report.Frameworks) > 11 {
		t.Errorf("frameworks count = %d, want <= 11", len(report.Frameworks))
	}
}

func TestExecutiveReport_HTMLContent(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	fi := fleet.NewFleetIndex()

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive.html", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	queries := db.New(openMigratedTestDB(t))
	fi := fleet.NewFleetIndex()

	_, mux := newExecutiveTestHandler(t, fi, queries)

	// JSON download.
	req := httptest.NewRequest("GET", "/api/v1/report/executive?download=1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "vigo-executive-") {
		t.Errorf("JSON Content-Disposition = %q, want attachment with vigo-executive-", cd)
	}

	// HTML download.
	req = httptest.NewRequest("GET", "/api/v1/report/executive.html?download=1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	cd = rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".html") {
		t.Errorf("HTML Content-Disposition = %q, want attachment with .html", cd)
	}
}

func TestExecutiveReport_EmptyFleet(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	fi := fleet.NewFleetIndex()

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	queries := db.New(openMigratedTestDB(t))
	fi := fleet.NewFleetIndex()

	fi.AddEnvoy(fleet.EnvoyState{ID: "e1", Hostname: "ok.example.com", ConvergenceStatus: "converged", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e2", Hostname: "err.example.com", ConvergenceStatus: "failed", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e3", Hostname: "stale.example.com", ConvergenceStatus: "converged", Offline: true, LastSeen: time.Now().Add(-24 * time.Hour)}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e4", Hostname: "drift.example.com", ConvergenceStatus: "degraded", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e5", Hostname: "new.example.com", ConvergenceStatus: "no data", LastSeen: time.Now()}, nil)

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	fi := fleet.NewFleetIndex()

	// The trend comes from compliance_history (per-framework score_percent
	// over time), not convergence_history. BuildExecutiveReport averages
	// the score_percent across frameworks per day. Insert in arbitrary
	// chronological order — the DB returns DESC and the handler reverses.
	dates := []string{"2026-03-01", "2026-03-02", "2026-03-03"}
	for _, d := range dates {
		// Two frameworks per day, both at 80 — the per-day average is 80.
		for _, fw := range []string{"hipaa", "soc2"} {
			_, err := sqlDB.Exec(
				`INSERT INTO compliance_history (snapshot_date, framework, score_percent, satisfied, partial, not_satisfied, total_controls) VALUES (?, ?, 80, 8, 0, 2, 10)`,
				d, fw,
			)
			if err != nil {
				t.Fatalf("insert history: %v", err)
			}
		}
	}

	_, mux := newExecutiveTestHandler(t, fi, queries)

	req := httptest.NewRequest("GET", "/api/v1/report/executive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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

	// Each day: average of hipaa(80) and soc2(80) = 80.
	if report.Trend[0].Percent != 80 {
		t.Errorf("trend[0].percent = %v, want 80", report.Trend[0].Percent)
	}
}
