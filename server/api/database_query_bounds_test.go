// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runDBQuery(t *testing.T, h *DatabaseHandler, sql string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	body, _ := json.Marshal(map[string]string{"sql": sql})
	req := httptest.NewRequest("POST", "/api/v1/db/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))
	return rec
}

// TestDatabase_QueryAudited: every ad-hoc query lands in the audit chain
// with its SQL text.
func TestDatabase_QueryAudited(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)
	h := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	aw := &recordingAuditWriter{}
	h.SetAuditWriter(aw)

	rec := runDBQuery(t, h, "SELECT name FROM test_items")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(aw.events) != 1 || aw.events[0].EventType != "db.query" {
		t.Fatalf("want one db.query audit event, got %+v", aw.events)
	}
	if !strings.Contains(aw.events[0].Payload, "SELECT name FROM test_items") {
		t.Errorf("audit payload should carry the SQL, got %q", aw.events[0].Payload)
	}
}

// TestDatabase_QueryRowCap: a result larger than the cap is cut at the cap
// and marked truncated.
func TestDatabase_QueryRowCap(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)
	h := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)

	rec := runDBQuery(t, h, "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 10050) SELECT i FROM n")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Count     int  `json:"count"`
		Truncated bool `json:"truncated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != dbQueryMaxRows || !resp.Truncated {
		t.Errorf("count=%d truncated=%v, want %d true", resp.Count, resp.Truncated, dbQueryMaxRows)
	}
}

// TestDatabase_QueryRefusedWithoutReadOnlyConnection: off SQLite there is
// no write-refusing connection, so the endpoint refuses rather than run
// arbitrary SQL on the shared read-write handle.
func TestDatabase_QueryRefusedWithoutReadOnlyConnection(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)
	h := NewDatabaseHandler(sqlDB, queries, "postgres", "")
	rec := runDBQuery(t, h, "SELECT 1")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
