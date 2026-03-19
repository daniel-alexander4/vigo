// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/publish"
)

// openConfigTestDB creates an in-memory SQLite database with config-related tables.
func openConfigTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE config_publishes (
			id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))),
			operator      TEXT NOT NULL DEFAULT '',
			published_at  DATETIME NOT NULL DEFAULT (datetime('now')),
			changed_files TEXT NOT NULL DEFAULT '[]',
			removed_files TEXT NOT NULL DEFAULT '[]',
			diff_summary  TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX idx_config_publishes_published_at ON config_publishes (published_at);

		CREATE TABLE envoys (
			id                       TEXT PRIMARY KEY,
			hostname                 TEXT NOT NULL,
			ip_address               TEXT NOT NULL,
			pubkey_pem               TEXT NOT NULL,
			enrolled_at              DATETIME NOT NULL DEFAULT (datetime('now')),
			last_seen                DATETIME,
			revoked                  INTEGER NOT NULL DEFAULT 0,
			revoked_at               DATETIME,
			force_push               INTEGER NOT NULL DEFAULT 0,
			pending_rotated_secrets  TEXT NOT NULL DEFAULT '[]',
			tags                     TEXT NOT NULL DEFAULT '',
			maintenance_until        DATETIME
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

func TestConfig_RecordPublish(t *testing.T) {
	_, queries := openConfigTestDB(t)

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"operator":"alice","changed_files":["modules/nginx.vgo"],"removed_files":[],"diff_summary":"updated nginx module"}`
	req := httptest.NewRequest("POST", "/api/v1/config/publishes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["id"] == nil || resp["id"] == "" {
		t.Error("expected publish ID to be returned")
	}
}

func TestConfig_ListPublishes(t *testing.T) {
	sqlDB, queries := openConfigTestDB(t)

	// Insert some publishes directly.
	for i := 0; i < 3; i++ {
		_, err := sqlDB.Exec(
			`INSERT INTO config_publishes (operator, changed_files, removed_files) VALUES (?, ?, ?)`,
			"admin", `["file.vgo"]`, `[]`,
		)
		if err != nil {
			t.Fatalf("insert publish: %v", err)
		}
	}

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/publishes?limit=2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Publishes []map[string]any `json:"publishes"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Publishes) != 2 {
		t.Errorf("expected 2 publishes with limit=2, got %d", len(resp.Publishes))
	}
}

func TestConfig_GetPublish(t *testing.T) {
	sqlDB, queries := openConfigTestDB(t)

	var publishID string
	err := sqlDB.QueryRow(
		`INSERT INTO config_publishes (operator, changed_files, removed_files, diff_summary) VALUES (?, ?, ?, ?) RETURNING id`,
		"alice", `["modules/nginx.vgo"]`, `["modules/old.vgo"]`, "updated nginx",
	).Scan(&publishID)
	if err != nil {
		t.Fatalf("insert publish: %v", err)
	}

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/publishes/"+publishID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Publish map[string]any `json:"publish"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Publish["operator"] != "alice" {
		t.Errorf("expected operator alice, got %v", resp.Publish["operator"])
	}
	if resp.Publish["diff_summary"] != "updated nginx" {
		t.Errorf("expected diff_summary, got %v", resp.Publish["diff_summary"])
	}
}

func TestConfig_GetPublishNotFound(t *testing.T) {
	_, queries := openConfigTestDB(t)

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/publishes/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestConfig_Freeze(t *testing.T) {
	_, queries := openConfigTestDB(t)

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	until := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"freeze_until":"` + until + `"}`
	req := httptest.NewRequest("POST", "/api/v1/config/freeze", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["freeze_until"] == nil {
		t.Error("expected freeze_until to be set")
	}

	// Verify the global freeze is set.
	if publish.FreezeUntil == nil {
		t.Error("expected publish.FreezeUntil to be set")
	}

	// Clean up.
	publish.FreezeUntil = nil
}

func TestConfig_FreezeInvalidTimestamp(t *testing.T) {
	_, queries := openConfigTestDB(t)

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"freeze_until":"not-a-timestamp"}`
	req := httptest.NewRequest("POST", "/api/v1/config/freeze", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestConfig_Unfreeze(t *testing.T) {
	_, queries := openConfigTestDB(t)

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Set a freeze first.
	now := time.Now().Add(1 * time.Hour)
	publish.FreezeUntil = &now

	req := httptest.NewRequest("POST", "/api/v1/config/unfreeze", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if publish.FreezeUntil != nil {
		t.Error("expected publish.FreezeUntil to be nil after unfreeze")
	}
}

func TestConfig_ReloadBlockedByApproval(t *testing.T) {
	_, queries := openConfigTestDB(t)

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), nil)
	handler.publishCfg = config.PublishSection{ApprovalRequired: true}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/config/reload", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when approval required, got %d: %s", rec.Code, rec.Body.String())
	}
}
