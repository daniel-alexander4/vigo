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

	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
)

// dbTestDSN is the shared-cache in-memory DSN used by database handler tests.
// It is the value passed to NewDatabaseHandler so the handler's read-only
// /db/query connection reopens the *same* shared-cache DB (mirroring how, in
// production, the RO handle reopens the same file path).
const dbTestDSN = "file:dbtest?mode=memory&cache=shared&_foreign_keys=on"

// openDatabaseTestDB creates a shared-cache in-memory SQLite database with
// a simple table for database handler tests. The file: URI with mode=memory
// and shared cache ensures all connections in the pool see the same data.
func openDatabaseTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", dbTestDSN)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS test_items (
			id   INTEGER PRIMARY KEY,
			name TEXT NOT NULL
		);
		DELETE FROM test_items;
		INSERT INTO test_items (name) VALUES ('alpha'), ('beta'), ('gamma');
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

func TestDatabase_Stats(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/db/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["driver"] != "sqlite" {
		t.Errorf("expected driver sqlite, got %v", resp["driver"])
	}
	tables, ok := resp["tables"].(map[string]any)
	if !ok {
		t.Fatal("expected tables in response")
	}
	if tables["test_items"].(float64) != 3 {
		t.Errorf("expected 3 rows in test_items, got %v", tables["test_items"])
	}
}

func TestDatabase_Integrity(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/db/integrity", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}
	if resp["driver"] != "sqlite" {
		t.Errorf("expected driver sqlite, got %v", resp["driver"])
	}
}

func TestDatabase_Checkpoint(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/db/checkpoint", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %v", resp["status"])
	}
}

func TestDatabase_QueryReadOnly(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"sql":"SELECT name FROM test_items ORDER BY name"}`
	req := httptest.NewRequest("POST", "/api/v1/db/query", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Columns []string         `json:"columns"`
		Rows    []map[string]any `json:"rows"`
		Count   int              `json:"count"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Count != 3 {
		t.Errorf("expected 3 rows, got %d", resp.Count)
	}
	if len(resp.Columns) != 1 || resp.Columns[0] != "name" {
		t.Errorf("expected columns [name], got %v", resp.Columns)
	}
}

func TestDatabase_QueryBlocksWrites(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	for _, stmt := range []string{
		`INSERT INTO test_items (name) VALUES ('bad')`,
		`UPDATE test_items SET name='bad'`,
		`DELETE FROM test_items`,
		`DROP TABLE test_items`,
		`ALTER TABLE test_items ADD COLUMN bad TEXT`,
		`CREATE TABLE bad (id INTEGER)`,
	} {
		body := `{"sql":"` + stmt + `"}`
		req := httptest.NewRequest("POST", "/api/v1/db/query", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, adminReq(req))

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for %q, got %d", stmt, rec.Code)
		}
	}
}

// TestDatabase_QueryReadOnlyBlocksCTEWrite proves the C22 fix: a write that
// slips past the keyword-prefix denylist (it starts with WITH, not DELETE) is
// still rejected because the query runs on a query_only=on connection.
func TestDatabase_QueryReadOnlyBlocksCTEWrite(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Starts with WITH so the prefix denylist passes it through; the RO
	// connection must reject the embedded DELETE.
	body := `{"sql":"WITH x AS (DELETE FROM test_items RETURNING *) SELECT * FROM x"}`
	req := httptest.NewRequest("POST", "/api/v1/db/query", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code == http.StatusOK {
		t.Fatalf("expected the write to be rejected, got 200: %s", rec.Body.String())
	}

	// And the data must be untouched.
	if got := countTestItems(t, sqlDB); got != 3 {
		t.Fatalf("expected 3 rows intact after blocked write, got %d", got)
	}
}

// TestDatabase_QueryRejectsMultipleStatements proves interior-semicolon
// multi-statement input is refused before execution.
func TestDatabase_QueryRejectsMultipleStatements(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"sql":"SELECT 1; DELETE FROM test_items"}`
	req := httptest.NewRequest("POST", "/api/v1/db/query", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for multi-statement, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countTestItems(t, sqlDB); got != 3 {
		t.Fatalf("expected 3 rows intact, got %d", got)
	}
}

func countTestItems(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM test_items").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestDatabase_QueryMissingSQL(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "sqlite", dbTestDSN)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"sql":""}`
	req := httptest.NewRequest("POST", "/api/v1/db/query", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDatabase_CheckpointNonSQLite(t *testing.T) {
	sqlDB, queries := openDatabaseTestDB(t)

	handler := NewDatabaseHandler(sqlDB, queries, "postgres", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/db/checkpoint", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-SQLite checkpoint, got %d", rec.Code)
	}
}
