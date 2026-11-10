// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	_ "github.com/mattn/go-sqlite3"
)

// fixedConvergenceDS overrides ConvergenceData with a fleet where offline
// envoys also carry failure-axis statuses (the axes are orthogonal).
type fixedConvergenceDS struct {
	*mockDataSource
	page ConvergencePageData
}

func (f fixedConvergenceDS) ConvergenceData(time.Duration, time.Duration) ConvergencePageData {
	return f.page
}

// TestEvidenceSnapshot_LiveExcludesOffline: with no persisted snapshot the
// fragment falls back to live data and must report the data source's active-
// fleet percentage. 10 envoys, 6 converged + 4 failed, 4 offline (2 of each):
// the SoT value is 6/(10−4) = 100%; dividing by the failure-axis sum gives 60%.
func TestEvidenceSnapshot_LiveExcludesOffline(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", "file:evsnaplive?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	h := newTestHandler(t)
	h.queries = db.New(sqlDB) // no convergence_history table → live fallback
	h.dataSource = fixedConvergenceDS{
		mockDataSource: &mockDataSource{data: defaultTestData()},
		page:           ConvergencePageData{ConvergencePct: 100, Total: 10, Converged: 6, Failed: 4, Offline: 4},
	}
	rec := httptest.NewRecorder()
	h.EvidenceSnapshotFragment(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/evidence/snapshot?date=2026-10-01", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "(live)") || !strings.Contains(body, "100%") {
		t.Errorf("live snapshot pct should be 100%% (6 converged / 6 active); body:\n%s", body)
	}
}

// TestEvidenceSnapshot_ExcludesOffline proves the persisted-snapshot branch of
// the evidence-snapshot fragment divides converged by the ACTIVE fleet
// (total − offline), the same denominator as the live branch and every other
// convergence surface. 6 converged of 10 total with 4 offline is 100%, not 60%.
func TestEvidenceSnapshot_ExcludesOffline(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", "file:evsnaptest?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`
		CREATE TABLE convergence_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			recorded_at TEXT NOT NULL,
			total INTEGER NOT NULL, converged INTEGER NOT NULL, degraded INTEGER NOT NULL,
			failed INTEGER NOT NULL, no_data INTEGER NOT NULL, changed INTEGER NOT NULL,
			diverged INTEGER NOT NULL, offline INTEGER NOT NULL);
		INSERT INTO convergence_history(recorded_at,total,converged,degraded,failed,no_data,changed,diverged,offline)
		VALUES ('2026-10-01',10,6,0,0,0,0,0,4);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	h := newTestHandler(t)
	h.queries = db.New(sqlDB)

	rec := httptest.NewRecorder()
	h.EvidenceSnapshotFragment(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/evidence/snapshot?date=2026-10-01", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "100%") {
		t.Errorf("snapshot pct should be 100%% (6 converged / 6 active); body:\n%s", body)
	}
}
