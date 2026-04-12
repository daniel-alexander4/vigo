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

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// openEnvoysTestDB creates an in-memory SQLite database with the envoys table.
func openEnvoysTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
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
			maintenance_until        DATETIME,
			agent_version            TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

func TestEnvoys_SetTags(t *testing.T) {
	sqlDB, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	// Insert a envoy in the DB and FleetIndex.
	envoyID := "t-001"
	_, err := sqlDB.Exec(
		`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		envoyID, "web01.prod", "10.0.0.1", "pubkey-pem",
	)
	if err != nil {
		t.Fatalf("insert envoy: %v", err)
	}
	fi.AddEnvoy(fleet.EnvoyState{
		ID: envoyID, Hostname: "web01.prod", IP: "10.0.0.1",
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"tags":["web","production"]}`
	req := httptest.NewRequest("PUT", "/api/v1/envoys/"+envoyID+"/tags", bytes.NewBufferString(body))
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

func TestEnvoys_GetTags(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	envoyID := "t-002"
	fi.AddEnvoy(fleet.EnvoyState{
		ID: envoyID, Hostname: "web02.prod", IP: "10.0.0.2",
		Tags:       []string{"staging", "db"},
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/envoys/"+envoyID+"/tags", nil)
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

func TestEnvoys_GetTagsNotFound(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/envoys/nonexistent/tags", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestEnvoys_SetMaintenance(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	envoyID := "t-003"
	fi.AddEnvoy(fleet.EnvoyState{
		ID: envoyID, Hostname: "web03.prod", IP: "10.0.0.3",
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"until":"` + until + `"}`
	req := httptest.NewRequest("PUT", "/api/v1/envoys/"+envoyID+"/maintenance", bytes.NewBufferString(body))
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

func TestEnvoys_SetMaintenanceInvalidTimestamp(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"until":"not-a-timestamp"}`
	req := httptest.NewRequest("PUT", "/api/v1/envoys/t-001/maintenance", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestEnvoys_ClearMaintenance(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	envoyID := "t-004"
	fi.AddEnvoy(fleet.EnvoyState{
		ID: envoyID, Hostname: "web04.prod", IP: "10.0.0.4",
		EnrolledAt:       time.Now().UTC(),
		LastSeen:         time.Now().UTC(),
		MaintenanceUntil: time.Now().Add(1 * time.Hour),
	}, nil)

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("DELETE", "/api/v1/envoys/"+envoyID+"/maintenance", nil)
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

func TestEnvoys_GetConvergence(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	envoyID := "t-005"
	fi.AddEnvoy(fleet.EnvoyState{
		ID: envoyID, Hostname: "web05.prod", IP: "10.0.0.5",
		ConvergenceStatus: "converged",
		EnrolledAt:       time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/envoys/"+envoyID+"/convergence", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "converged" {
		t.Errorf("expected converged, got %v", resp["status"])
	}
}

func TestEnvoys_GetConvergenceNotFound(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/envoys/nonexistent/convergence", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestEnvoys_ForceUpdate(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	envoyID := "t-006"
	fi.AddEnvoy(fleet.EnvoyState{
		ID: envoyID, Hostname: "web06.prod", IP: "10.0.0.6",
		EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}, nil)

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/envoys/"+envoyID+"/update", nil)
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

func TestEnvoys_ForceUpdateNotFound(t *testing.T) {
	_, queries := openEnvoysTestDB(t)
	fi := fleet.NewFleetIndex()

	handler := NewEnvoysHandler(queries, fi)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/envoys/nonexistent/update", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
