// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/db"
)

// openAuditTestDB creates an in-memory SQLite database with tables needed for audit tests.
func openAuditTestDB(t *testing.T) (*sql.DB, *db.Queries) {
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
			source      TEXT NOT NULL DEFAULT '',
			summary     TEXT NOT NULL DEFAULT '',
			payload     TEXT NOT NULL DEFAULT '{}'
		);
		CREATE INDEX idx_audit_entries_timestamp ON audit_entries (timestamp);
		CREATE INDEX idx_audit_entries_event_type ON audit_entries (event_type);

		CREATE TABLE config_publishes (
			id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			operator      TEXT NOT NULL DEFAULT '',
			published_at  DATETIME NOT NULL DEFAULT (datetime('now')),
			changed_files TEXT NOT NULL DEFAULT '[]',
			removed_files TEXT NOT NULL DEFAULT '[]',
			diff_summary  TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE push_runs (
			id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			command       TEXT NOT NULL,
			target_glob   TEXT NOT NULL,
			operator      TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'cancelled')) DEFAULT 'pending',
			total_targets INTEGER NOT NULL DEFAULT 0,
			created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
			started_at    DATETIME,
			finished_at   DATETIME,
			timeout_secs  INTEGER NOT NULL DEFAULT 60,
			batch_size    TEXT,
			max_failures  TEXT
		);

		CREATE TABLE workflow_runs (
			id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			workflow_name   TEXT NOT NULL,
			status          TEXT NOT NULL CHECK (status IN ('pending', 'running', 'complete', 'failed', 'aborted')) DEFAULT 'pending',
			current_step    INTEGER NOT NULL DEFAULT 0,
			total_steps     INTEGER NOT NULL DEFAULT 0,
			operator        TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
			started_at      DATETIME,
			finished_at     DATETIME,
			failure_reason  TEXT
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

// insertAuditEntry inserts a properly hash-chained audit entry.
func insertAuditEntry(t *testing.T, sqlDB *sql.DB, prevHash, eventType, actor, summary string, ts time.Time) string {
	t.Helper()
	tsStr := ts.UTC().Format(time.RFC3339)
	source := ""
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte(tsStr))
	h.Write([]byte(eventType))
	h.Write([]byte(actor))
	h.Write([]byte(source))
	h.Write([]byte(summary))
	h.Write([]byte("{}"))
	hash := hex.EncodeToString(h.Sum(nil))

	_, err := sqlDB.Exec(
		`INSERT INTO audit_entries (prev_hash, hash, timestamp, event_type, actor, source, summary, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		prevHash, hash, ts, eventType, actor, source, summary, "{}",
	)
	if err != nil {
		t.Fatalf("insert audit entry: %v", err)
	}
	return hash
}

func TestAudit_ListEvents(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	h1 := insertAuditEntry(t, sqlDB, "", "config.reload", "alice", "config reloaded", now.Add(-2*time.Minute))
	insertAuditEntry(t, sqlDB, h1, "config.reload", "bob", "config reloaded again", now.Add(-1*time.Minute))

	handler := NewAuditHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Events []auditEvent `json:"events"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if len(resp.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(resp.Events))
	}
	// Should be sorted by timestamp descending.
	if resp.Events[0].Operator != "bob" {
		t.Errorf("expected bob first (more recent), got %s", resp.Events[0].Operator)
	}
}

func TestAudit_ListEventsWithTypeFilter(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	insertAuditEntry(t, sqlDB, "", "config.reload", "alice", "config reloaded", now)

	// Insert a push run.
	_, err := sqlDB.Exec(
		`INSERT INTO push_runs (id, command, target_glob, operator, status) VALUES (?, ?, ?, ?, ?)`,
		"pr-001", "uptime", "*.web.*", "bob", "complete",
	)
	if err != nil {
		t.Fatalf("insert push run: %v", err)
	}

	handler := NewAuditHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Filter by push_run type only.
	req := httptest.NewRequest("GET", "/api/v1/audit?type=push_run", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Events []auditEvent `json:"events"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 push_run event, got %d", len(resp.Events))
	}
	if resp.Events[0].Type != "push_run" {
		t.Errorf("expected push_run type, got %s", resp.Events[0].Type)
	}
}

func TestAudit_ListEventsEmpty(t *testing.T) {
	_, queries := openAuditTestDB(t)

	handler := NewAuditHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Events []auditEvent `json:"events"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Events == nil {
		// Events may be nil (no events found), which is acceptable.
		return
	}
	if len(resp.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(resp.Events))
	}
}

func TestAudit_ListEventsWithLimit(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	h1 := insertAuditEntry(t, sqlDB, "", "config.reload", "alice", "event 1", now.Add(-3*time.Minute))
	h2 := insertAuditEntry(t, sqlDB, h1, "config.reload", "bob", "event 2", now.Add(-2*time.Minute))
	insertAuditEntry(t, sqlDB, h2, "config.reload", "charlie", "event 3", now.Add(-1*time.Minute))

	handler := NewAuditHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/audit?limit=2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Events []auditEvent `json:"events"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Events) != 2 {
		t.Errorf("expected 2 events with limit=2, got %d", len(resp.Events))
	}
}

func TestAudit_VerifyChainEmpty(t *testing.T) {
	_, queries := openAuditTestDB(t)

	handler := NewAuditHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/audit/verify", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["valid"] != true {
		t.Errorf("expected valid=true for empty chain, got %v", resp["valid"])
	}
}

func TestAudit_VerifyChainValid(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	h1 := insertAuditEntry(t, sqlDB, "", "config.reload", "alice", "event 1", now.Add(-2*time.Minute))
	insertAuditEntry(t, sqlDB, h1, "config.reload", "bob", "event 2", now.Add(-1*time.Minute))

	handler := NewAuditHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/audit/verify", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["valid"] != true {
		t.Errorf("expected valid=true, got %v", resp["valid"])
	}
	if resp["entry_count"].(float64) != 2 {
		t.Errorf("expected entry_count=2, got %v", resp["entry_count"])
	}
}
