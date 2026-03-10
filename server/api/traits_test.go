package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/hobnob/hobnob/server/db"
)

// openTestDB creates an in-memory SQLite database with the schema applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	// Minimal schema for traits tests.
	_, err = sqlDB.Exec(`
		CREATE TABLE nodes (
			id          TEXT PRIMARY KEY,
			hostname    TEXT NOT NULL,
			ip_address  TEXT NOT NULL,
			pubkey_pem  TEXT NOT NULL,
			enrolled_at DATETIME NOT NULL DEFAULT (datetime('now')),
			last_seen   DATETIME,
			revoked     INTEGER NOT NULL DEFAULT 0,
			revoked_at  DATETIME,
			force_push  INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE node_traits (
			id           TEXT PRIMARY KEY,
			node_id      TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
			traits       TEXT NOT NULL DEFAULT '{}',
			collected_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB
}

func TestTraitsHandler_Success(t *testing.T) {
	sqlDB := openTestDB(t)
	nodeID := uuid.New().String()

	// Insert a node.
	_, err := sqlDB.Exec(
		`INSERT INTO nodes (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		nodeID, "web01", "10.0.0.1", "pubkey-pem",
	)
	if err != nil {
		t.Fatalf("insert node: %v", err)
	}

	// Insert traits with a fixed timestamp.
	collectedAt := time.Date(2026, 3, 7, 10, 30, 0, 0, time.UTC)
	traitsJSON := `{"os":{"distro":"ubuntu","version":"22.04"}}`
	_, err = sqlDB.Exec(
		`INSERT INTO node_traits (id, node_id, traits, collected_at) VALUES (?, ?, ?, ?)`,
		uuid.New().String(), nodeID, traitsJSON, collectedAt.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert traits: %v", err)
	}

	handler := NewTraitsHandler(db.New(sqlDB))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/nodes/web01/traits", nil)
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
	sqlDB := openTestDB(t)
	handler := NewTraitsHandler(db.New(sqlDB))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/nodes/nonexistent/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["message"] != "node 'nonexistent' not found" {
		t.Errorf("expected node not found error, got %q", resp["message"])
	}
}

func TestTraitsHandler_NoFacts(t *testing.T) {
	sqlDB := openTestDB(t)
	nodeID := uuid.New().String()

	// Insert a node without traits.
	_, err := sqlDB.Exec(
		`INSERT INTO nodes (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		nodeID, "web01", "10.0.0.1", "pubkey-pem",
	)
	if err != nil {
		t.Fatalf("insert node: %v", err)
	}

	handler := NewTraitsHandler(db.New(sqlDB))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/nodes/web01/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	expected := "no traits available for 'web01' -- node has not reported traits yet"
	if resp["message"] != expected {
		t.Errorf("expected %q, got %q", expected, resp["message"])
	}
}
