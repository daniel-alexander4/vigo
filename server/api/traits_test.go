// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
)

func TestTraitsHandler_Success(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	envoyID := uuid.New().String()

	// Insert a envoy.
	_, err := sqlDB.Exec(
		`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		envoyID, "web01", "10.0.0.1", "pubkey-pem",
	)
	if err != nil {
		t.Fatalf("insert envoy: %v", err)
	}

	// Insert traits with a fixed timestamp.
	collectedAt := time.Date(2026, 3, 7, 10, 30, 0, 0, time.UTC)
	traitsJSON := `{"os":{"distro":"ubuntu","version":"22.04"}}`
	_, err = sqlDB.Exec(
		`INSERT INTO envoy_traits (id, envoy_id, traits, collected_at) VALUES (?, ?, ?, ?)`,
		uuid.New().String(), envoyID, traitsJSON, collectedAt.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert traits: %v", err)
	}

	handler := NewTraitsHandler(db.New(sqlDB))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/envoys/web01/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var resp struct {
		Hostname    string          `json:"hostname"`
		CollectedAt string          `json:"collected_at"`
		Traits      json.RawMessage `json:"traits"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Hostname != "web01" {
		t.Errorf("expected hostname web01, got %s", resp.Hostname)
	}

	// collected_at must be RFC3339
	if _, err := time.Parse(time.RFC3339, resp.CollectedAt); err != nil {
		t.Errorf("collected_at is not RFC3339: %s", resp.CollectedAt)
	}

	// traits must be the raw JSON (not double-encoded)
	var traits map[string]any
	if err := json.Unmarshal(resp.Traits, &traits); err != nil {
		t.Fatalf("traits is not valid JSON: %v", err)
	}

	osMap, ok := traits["os"].(map[string]any)
	if !ok {
		t.Fatalf("expected os to be a map, got %T", traits["os"])
	}
	if osMap["distro"] != "ubuntu" {
		t.Errorf("expected os.distro=ubuntu, got %v", osMap["distro"])
	}
}

func TestTraitsHandler_NodeNotFound(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	handler := NewTraitsHandler(db.New(sqlDB))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/envoys/nonexistent/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["message"] != "envoy 'nonexistent' not found" {
		t.Errorf("expected envoy not found error, got %q", resp["message"])
	}
}

func TestTraitsHandler_NoFacts(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	envoyID := uuid.New().String()

	// Insert a envoy without traits.
	_, err := sqlDB.Exec(
		`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		envoyID, "web01", "10.0.0.1", "pubkey-pem",
	)
	if err != nil {
		t.Fatalf("insert envoy: %v", err)
	}

	handler := NewTraitsHandler(db.New(sqlDB))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/envoys/web01/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	expected := "no traits available for 'web01' -- envoy has not reported traits yet"
	if resp["message"] != expected {
		t.Errorf("expected %q, got %q", expected, resp["message"])
	}
}
