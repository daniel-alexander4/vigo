// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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
