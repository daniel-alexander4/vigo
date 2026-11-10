// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
)

func seedInventory(t *testing.T) (*http.ServeMux, *db.Queries) {
	t.Helper()
	sqlDB := openMigratedTestDB(t)

	// Insert three envoys with different traits.
	for _, tc := range []struct {
		hostname string
		ip       string
		traits   string
	}{
		{"web01", "10.0.0.1", `{"os":{"distro":"ubuntu","version":"22.04","family":"debian"},"network":{"ip":"10.0.0.1"}}`},
		{"web02", "10.0.0.2", `{"os":{"distro":"ubuntu","version":"24.04","family":"debian"},"network":{"ip":"10.0.0.2"}}`},
		{"db01", "10.0.1.1", `{"os":{"distro":"rocky","version":"9.3","family":"rhel"},"network":{"ip":"10.0.1.1"}}`},
	} {
		id := uuid.New().String()
		_, err := sqlDB.Exec(
			`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
			id, tc.hostname, tc.ip, "pubkey-pem",
		)
		if err != nil {
			t.Fatalf("insert envoy %s: %v", tc.hostname, err)
		}
		_, err = sqlDB.Exec(
			`INSERT INTO envoy_traits (id, envoy_id, traits) VALUES (?, ?, ?)`,
			uuid.New().String(), id, tc.traits,
		)
		if err != nil {
			t.Fatalf("insert traits for %s: %v", tc.hostname, err)
		}
	}

	// Insert a revoked envoy (should be excluded).
	revokedID := uuid.New().String()
	sqlDB.Exec(`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem, revoked) VALUES (?, ?, ?, ?, 1)`,
		revokedID, "revoked01", "10.0.2.1", "pubkey-pem")
	sqlDB.Exec(`INSERT INTO envoy_traits (id, envoy_id, traits) VALUES (?, ?, ?)`,
		uuid.New().String(), revokedID, `{"os":{"distro":"ubuntu"}}`)

	queries := db.New(sqlDB)
	handler := NewInventoryHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux, queries
}

func TestInventory_NoFilter(t *testing.T) {
	mux, _ := seedInventory(t)

	req := httptest.NewRequest("GET", "/api/v1/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Count  int              `json:"count"`
		Envoys []map[string]any `json:"envoys"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Count != 3 {
		t.Errorf("expected 3 envoys, got %d", resp.Count)
	}
}

func TestInventory_WhereFilter(t *testing.T) {
	mux, _ := seedInventory(t)

	req := httptest.NewRequest("GET", "/api/v1/inventory?where=os.distro%3Dubuntu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp struct {
		Count  int `json:"count"`
		Envoys []struct {
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Count != 2 {
		t.Fatalf("expected 2 ubuntu envoys, got %d", resp.Count)
	}
	for _, th := range resp.Envoys {
		if th.Hostname != "web01" && th.Hostname != "web02" {
			t.Errorf("unexpected hostname %s", th.Hostname)
		}
	}
}

func TestInventory_MultipleWhere(t *testing.T) {
	mux, _ := seedInventory(t)

	req := httptest.NewRequest("GET", "/api/v1/inventory?where=os.distro%3Dubuntu&where=os.version%3D24.04", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp struct {
		Count  int `json:"count"`
		Envoys []struct {
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Count != 1 {
		t.Fatalf("expected 1 envoy, got %d", resp.Count)
	}
	if resp.Envoys[0].Hostname != "web02" {
		t.Errorf("expected web02, got %s", resp.Envoys[0].Hostname)
	}
}

func TestInventory_ShowFields(t *testing.T) {
	mux, _ := seedInventory(t)

	req := httptest.NewRequest("GET", "/api/v1/inventory?where=os.family%3Drhel&show=os.distro,network.ip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp struct {
		Count  int `json:"count"`
		Envoys []struct {
			Hostname string         `json:"hostname"`
			Traits   map[string]any `json:"traits"`
		} `json:"envoys"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Count != 1 {
		t.Fatalf("expected 1 rhel envoy, got %d", resp.Count)
	}

	traits := resp.Envoys[0].Traits
	if traits["os.distro"] != "rocky" {
		t.Errorf("expected os.distro=rocky, got %v", traits["os.distro"])
	}
	if traits["network.ip"] != "10.0.1.1" {
		t.Errorf("expected network.ip=10.0.1.1, got %v", traits["network.ip"])
	}
	// Should not include other fields.
	if _, ok := traits["os.version"]; ok {
		t.Errorf("did not expect os.version in output")
	}
}

func TestInventory_NoMatch(t *testing.T) {
	mux, _ := seedInventory(t)

	req := httptest.NewRequest("GET", "/api/v1/inventory?where=os.distro%3Dwindows", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp struct {
		Count int `json:"count"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Count != 0 {
		t.Errorf("expected 0 matches, got %d", resp.Count)
	}
}

func TestInventory_RevokedExcluded(t *testing.T) {
	mux, _ := seedInventory(t)

	// Query all — revoked envoy should not appear.
	req := httptest.NewRequest("GET", "/api/v1/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp struct {
		Count  int `json:"count"`
		Envoys []struct {
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	for _, th := range resp.Envoys {
		if th.Hostname == "revoked01" {
			t.Errorf("revoked envoy should not appear in inventory")
		}
	}
}

func TestInventory_InvalidWhere(t *testing.T) {
	mux, _ := seedInventory(t)

	req := httptest.NewRequest("GET", "/api/v1/inventory?where=invalid", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
