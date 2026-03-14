// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
	"github.com/hobnob/hobnob/server/secrets"
)

// openExportTestDB creates an in-memory SQLite database with enough schema
// for export tests (thralls + runs + run_results).
func openExportTestDB(t *testing.T) *sql.DB {
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
		CREATE TABLE runs (
			id            TEXT PRIMARY KEY,
			thrall_id     TEXT NOT NULL REFERENCES thralls (id) ON DELETE CASCADE,
			started_at    DATETIME NOT NULL DEFAULT (datetime('now')),
			finished_at   DATETIME,
			status        TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial')),
			module_count  INTEGER NOT NULL DEFAULT 0,
			changed_count INTEGER NOT NULL DEFAULT 0,
			failed_count  INTEGER NOT NULL DEFAULT 0,
			skipped_count INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE run_results (
			id             TEXT PRIMARY KEY,
			run_id         TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
			module_name    TEXT NOT NULL,
			resource_type  TEXT NOT NULL,
			resource_name  TEXT NOT NULL,
			action_taken   TEXT NOT NULL,
			changed        INTEGER NOT NULL DEFAULT 0,
			error_message  TEXT,
			duration_ms    INTEGER NOT NULL DEFAULT 0,
			created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB
}

// insertExportTestData inserts a thrall, run, and run results for testing.
// Returns the thrall ID and run ID.
func insertExportTestData(t *testing.T, sqlDB *sql.DB, hostname string, resultTime time.Time) (string, string) {
	t.Helper()
	thrallID := uuid.New().String()
	runID := uuid.New().String()

	_, err := sqlDB.Exec(
		`INSERT INTO thralls (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		thrallID, hostname, "10.0.0.1", "pubkey-pem",
	)
	if err != nil {
		t.Fatalf("insert thrall: %v", err)
	}

	_, err = sqlDB.Exec(
		`INSERT INTO runs (id, thrall_id, status, module_count) VALUES (?, ?, 'success', 2)`,
		runID, thrallID,
	)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	ts := resultTime.Format("2006-01-02 15:04:05")
	_, err = sqlDB.Exec(
		`INSERT INTO run_results (id, run_id, module_name, resource_type, resource_name, action_taken, changed, duration_ms, created_at)
		 VALUES (?, ?, 'nginx', 'package', 'nginx-pkg', 'install', 1, 150, ?)`,
		uuid.New().String(), runID, ts,
	)
	if err != nil {
		t.Fatalf("insert run_result: %v", err)
	}

	_, err = sqlDB.Exec(
		`INSERT INTO run_results (id, run_id, module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms, created_at)
		 VALUES (?, ?, 'nginx', 'file', 'nginx-conf', 'create', 0, 'permission denied', 45, ?)`,
		uuid.New().String(), runID, ts,
	)
	if err != nil {
		t.Fatalf("insert run_result: %v", err)
	}

	return thrallID, runID
}

func TestExportSIEMBasic(t *testing.T) {
	sqlDB := openExportTestDB(t)
	now := time.Now().UTC()
	insertExportTestData(t, sqlDB, "web01", now)

	handler := NewExportHandler(db.New(sqlDB), fleet.NewFleetIndex(), nil, config.ExportSection{SIEM: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/siem", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var events []SIEMEvent
	if err := json.NewDecoder(rec.Body).Decode(&events); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Check first event fields.
	found := false
	for _, e := range events {
		if e.ResourceName == "nginx-pkg" {
			found = true
			if e.Hostname != "web01" {
				t.Errorf("expected hostname web01, got %s", e.Hostname)
			}
			if e.Source != "hobnob" {
				t.Errorf("expected source hobnob, got %s", e.Source)
			}
			if !e.Changed {
				t.Error("expected changed=true for nginx-pkg")
			}
			if e.Action != "install" {
				t.Errorf("expected action install, got %s", e.Action)
			}
			if e.RunStatus != "success" {
				t.Errorf("expected run_status success, got %s", e.RunStatus)
			}
			if _, err := time.Parse(time.RFC3339, e.Timestamp); err != nil {
				t.Errorf("timestamp not RFC3339: %s", e.Timestamp)
			}
		}
	}
	if !found {
		t.Error("nginx-pkg event not found")
	}

	// Check error event.
	for _, e := range events {
		if e.ResourceName == "nginx-conf" {
			if e.Error != "permission denied" {
				t.Errorf("expected error 'permission denied', got %q", e.Error)
			}
			if e.Changed {
				t.Error("expected changed=false for nginx-conf")
			}
		}
	}
}

func TestExportSIEMSinceFilter(t *testing.T) {
	sqlDB := openExportTestDB(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	recent := time.Now().UTC()
	insertExportTestData(t, sqlDB, "web-old", old)
	insertExportTestData(t, sqlDB, "web-new", recent)

	handler := NewExportHandler(db.New(sqlDB), fleet.NewFleetIndex(), nil, config.ExportSection{SIEM: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Request with since=1h ago — should only get recent results.
	since := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest("GET", "/api/v1/export/siem?since="+since, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var events []SIEMEvent
	json.NewDecoder(rec.Body).Decode(&events)

	for _, e := range events {
		if e.Hostname == "web-old" {
			t.Errorf("old event should have been filtered out")
		}
	}
	if len(events) != 2 {
		t.Errorf("expected 2 recent events, got %d", len(events))
	}
}

func TestExportSIEMThrallFilter(t *testing.T) {
	sqlDB := openExportTestDB(t)
	now := time.Now().UTC()
	insertExportTestData(t, sqlDB, "web01", now)
	insertExportTestData(t, sqlDB, "db01", now)

	handler := NewExportHandler(db.New(sqlDB), fleet.NewFleetIndex(), nil, config.ExportSection{SIEM: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/siem?thrall=web01", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var events []SIEMEvent
	json.NewDecoder(rec.Body).Decode(&events)

	for _, e := range events {
		if e.Hostname != "web01" {
			t.Errorf("expected only web01 events, got %s", e.Hostname)
		}
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events for web01, got %d", len(events))
	}
}

func TestExportSIEMLimit(t *testing.T) {
	sqlDB := openExportTestDB(t)
	now := time.Now().UTC()
	insertExportTestData(t, sqlDB, "web01", now) // inserts 2 results

	handler := NewExportHandler(db.New(sqlDB), fleet.NewFleetIndex(), nil, config.ExportSection{SIEM: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/siem?limit=1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var events []SIEMEvent
	json.NewDecoder(rec.Body).Decode(&events)

	if len(events) != 1 {
		t.Errorf("expected 1 event with limit=1, got %d", len(events))
	}
}

func TestExportCMDBBasic(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddThrall(fleet.ThrallState{
		ID:               "t1",
		Hostname:         "web01.prod",
		IP:               "10.0.0.1",
		EnrolledAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeen:         time.Now().UTC(),
		ComplianceStatus: "compliant",
		Traits: map[string]string{
			"os.family":             "debian",
			"os.version":            "22.04",
			"hardware.architecture": "amd64",
			"kernel.version":        "6.8.0",
			"hardware.cpu_count":    "4",
			"memory.total":          "16384",
			"uptime.seconds":        "86400",
		},
	}, nil)

	handler := NewExportHandler(nil, fi, nil, config.ExportSection{CMDB: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cmdb", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var records []CMDBRecord
	if err := json.NewDecoder(rec.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	r := records[0]
	if r.Hostname != "web01.prod" {
		t.Errorf("hostname: expected web01.prod, got %s", r.Hostname)
	}
	if r.ThrallID != "t1" {
		t.Errorf("thrall_id: expected t1, got %s", r.ThrallID)
	}
	if r.OS != "debian" {
		t.Errorf("os: expected debian, got %s", r.OS)
	}
	if r.Architecture != "amd64" {
		t.Errorf("architecture: expected amd64, got %s", r.Architecture)
	}
	if r.ComplianceStatus != "compliant" {
		t.Errorf("compliance_status: expected compliant, got %s", r.ComplianceStatus)
	}
	if r.EnrolledAt == "" {
		t.Error("enrolled_at should not be empty")
	}
}

func TestExportCMDBStatusFilter(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddThrall(fleet.ThrallState{
		ID: "t1", Hostname: "web01", IP: "10.0.0.1",
		ComplianceStatus: "compliant", LastSeen: time.Now().UTC(),
		EnrolledAt: time.Now().UTC(),
	}, nil)
	fi.AddThrall(fleet.ThrallState{
		ID: "t2", Hostname: "web02", IP: "10.0.0.2",
		ComplianceStatus: "drifted", LastSeen: time.Now().UTC(),
		EnrolledAt: time.Now().UTC(),
	}, nil)

	handler := NewExportHandler(nil, fi, nil, config.ExportSection{CMDB: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cmdb?status=drifted", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var records []CMDBRecord
	json.NewDecoder(rec.Body).Decode(&records)

	if len(records) != 1 {
		t.Fatalf("expected 1 drifted record, got %d", len(records))
	}
	if records[0].Hostname != "web02" {
		t.Errorf("expected web02, got %s", records[0].Hostname)
	}
}

func TestExportCMDBEnvironmentFilter(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddThrall(fleet.ThrallState{
		ID: "t1", Hostname: "web01.prod", IP: "10.0.0.1",
		ComplianceStatus: "compliant", LastSeen: time.Now().UTC(),
		EnrolledAt: time.Now().UTC(),
	}, nil)
	fi.AddThrall(fleet.ThrallState{
		ID: "t2", Hostname: "web02.staging", IP: "10.0.0.2",
		ComplianceStatus: "compliant", LastSeen: time.Now().UTC(),
		EnrolledAt: time.Now().UTC(),
	}, nil)

	// Create a minimal config store with environment mappings.
	configStore := newTestConfigStore(t, map[string]string{
		"web01.prod":    "prod",
		"web02.staging": "staging",
	})

	handler := NewExportHandler(nil, fi, configStore, config.ExportSection{CMDB: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cmdb?environment=prod", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var records []CMDBRecord
	json.NewDecoder(rec.Body).Decode(&records)

	if len(records) != 1 {
		t.Fatalf("expected 1 prod record, got %d", len(records))
	}
	if records[0].Hostname != "web01.prod" {
		t.Errorf("expected web01.prod, got %s", records[0].Hostname)
	}
	if records[0].Environment != "prod" {
		t.Errorf("expected environment prod, got %s", records[0].Environment)
	}
}

func TestExportOSCALStub(t *testing.T) {
	handler := NewExportHandler(nil, fleet.NewFleetIndex(), nil, config.ExportSection{OSCAL: true})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/oscal", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var envelope map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	ar, ok := envelope["assessment-results"].(map[string]any)
	if !ok {
		t.Fatal("missing assessment-results key")
	}

	meta, ok := ar["metadata"].(map[string]any)
	if !ok {
		t.Fatal("missing metadata")
	}
	if meta["title"] != "Hobnob Assessment Results" {
		t.Errorf("unexpected title: %v", meta["title"])
	}
	if meta["oscal-version"] != "1.1.2" {
		t.Errorf("unexpected oscal-version: %v", meta["oscal-version"])
	}

	results, ok := ar["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatal("missing or empty results")
	}

	result := results[0].(map[string]any)
	findings, ok := result["findings"].([]any)
	if !ok {
		t.Fatal("missing findings")
	}
	if len(findings) != 0 {
		t.Errorf("expected empty findings, got %d", len(findings))
	}
}

// newTestConfigStore creates a ConfigStore backed by a temp directory with a
// nodes.hnb file mapping hostnames to environments. Each entry matches a specific
// hostname and sets the given environment.
func newTestConfigStore(t *testing.T, envMap map[string]string) *config.ConfigStore {
	t.Helper()
	dir := t.TempDir()

	// ConfigStore expects thralls/, modules/, and roles/ subdirectories.
	for _, sub := range []string{"thralls", "modules", "roles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Build thralls/nodes.hnb YAML.
	var entries []string
	for hostname, env := range envMap {
		entries = append(entries, fmt.Sprintf("  - match: %q\n    environment: %q", hostname, env))
	}
	nodesYAML := "thralls:\n" + strings.Join(entries, "\n")

	if err := os.WriteFile(filepath.Join(dir, "thralls", "nodes.hnb"), []byte(nodesYAML), 0644); err != nil {
		t.Fatalf("write nodes.hnb: %v", err)
	}

	// Use a local provider with no secrets (no secret: refs in our test config).
	provider, err := secrets.NewLocalProvider("", t.TempDir())
	if err != nil {
		t.Fatalf("new local provider: %v", err)
	}
	t.Cleanup(func() { provider.Close() })

	store, err := config.NewConfigStore(dir, provider, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	return store
}

func TestExportDisabledEndpoints(t *testing.T) {
	// All exports disabled — endpoints should not be registered.
	handler := NewExportHandler(nil, fleet.NewFleetIndex(), nil, config.ExportSection{})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	for _, path := range []string{
		"/api/v1/export/siem",
		"/api/v1/export/cmdb",
		"/api/v1/export/oscal",
	} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		// Unregistered routes on a bare ServeMux return 404.
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404 when disabled, got %d", path, rec.Code)
		}
	}
}
