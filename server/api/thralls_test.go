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

	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
)

// openThrallsTestDB creates an in-memory SQLite database with the thralls table.
func openThrallsTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE thralls (
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

func TestThralls_SetTags(t *testing.T) {
	sqlDB, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	// Insert a thrall in the DB and FleetIndex.
	thrallID := "t-001"
	_, err := sqlDB.Exec(
		`INSERT INTO thralls (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		thrallID, "web01.prod", "10.0.0.1", "pubkey-pem",
	)
	if err != nil {
		t.Fatalf("insert thrall: %v", err)
	}
	fi.AddThrall(fleet.ThrallState{
		ID: thrallID, Hostname: "web01.prod", IP: "10.0.0.1",
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"tags":["web","production"]}`
	req := httptest.NewRequest("PUT", "/api/v1/thralls/"+thrallID+"/tags", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	tags, ok := resp["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %v", resp["tags"])
	}
	if tags[0] != "web" || tags[1] != "production" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

func TestThralls_GetTags(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	thrallID := "t-002"
	fi.AddThrall(fleet.ThrallState{
		ID: thrallID, Hostname: "web02.prod", IP: "10.0.0.2",
		Tags:       []string{"staging", "db"},
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/thralls/"+thrallID+"/tags", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	tags, ok := resp["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %v", resp["tags"])
	}
}

func TestThralls_GetTagsNotFound(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/thralls/nonexistent/tags", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestThralls_SetMaintenance(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	thrallID := "t-003"
	fi.AddThrall(fleet.ThrallState{
		ID: thrallID, Hostname: "web03.prod", IP: "10.0.0.3",
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"until":"` + until + `"}`
	req := httptest.NewRequest("PUT", "/api/v1/thralls/"+thrallID+"/maintenance", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["maintenance_until"] == nil || resp["maintenance_until"] == "" {
		t.Error("expected maintenance_until to be set")
	}
}

func TestThralls_SetMaintenanceInvalidTimestamp(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"until":"not-a-timestamp"}`
	req := httptest.NewRequest("PUT", "/api/v1/thralls/t-001/maintenance", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestThralls_ClearMaintenance(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	thrallID := "t-004"
	fi.AddThrall(fleet.ThrallState{
		ID: thrallID, Hostname: "web04.prod", IP: "10.0.0.4",
		EnrolledAt:       time.Now().UTC(),
		LastSeen:         time.Now().UTC(),
		MaintenanceUntil: time.Now().Add(1 * time.Hour),
	}, nil)

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("DELETE", "/api/v1/thralls/"+thrallID+"/maintenance", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["maintenance_until"] != nil {
		t.Error("expected maintenance_until to be nil after clear")
	}
}

func TestThralls_GetCompliance(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	thrallID := "t-005"
	fi.AddThrall(fleet.ThrallState{
		ID: thrallID, Hostname: "web05.prod", IP: "10.0.0.5",
		ComplianceStatus: "compliant",
		EnrolledAt:       time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/thralls/"+thrallID+"/compliance", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "compliant" {
		t.Errorf("expected compliant, got %v", resp["status"])
	}
}

func TestThralls_GetComplianceNotFound(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/thralls/nonexistent/compliance", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestThralls_ForceUpdate(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	thrallID := "t-006"
	fi.AddThrall(fleet.ThrallState{
		ID: thrallID, Hostname: "web06.prod", IP: "10.0.0.6",
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/thralls/"+thrallID+"/update", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("expected status queued, got %v", resp["status"])
	}
}

func TestThralls_ForceUpdateNotFound(t *testing.T) {
	_, queries := openThrallsTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewThrallsHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/thralls/nonexistent/update", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
