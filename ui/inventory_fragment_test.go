// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/db"
	_ "github.com/mattn/go-sqlite3"
)

// TestInventoryFragment_RenderCap proves the inventory list fragment bounds its
// infinite scroll at inventoryRenderedCap: under the cap it emits the
// revealed-trigger sentinel carrying the running loaded count, and once the
// running count crosses the ceiling it emits a stop notice instead.
func TestInventoryFragment_RenderCap(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", "file:invcaptest?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`
		CREATE TABLE envoys (
			id         TEXT PRIMARY KEY,
			hostname   TEXT NOT NULL,
			ip_address TEXT NOT NULL DEFAULT '',
			revoked    INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE envoy_traits (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			envoy_id     TEXT NOT NULL,
			traits       TEXT NOT NULL DEFAULT '{}',
			collected_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// 201 envoys, one trait snapshot each — one past the 200/page batch so the
	// "has more" path (and thus the sentinel/cap branch) fires.
	for i := 0; i < 201; i++ {
		id := fmt.Sprintf("e-%03d", i)
		host := fmt.Sprintf("host-%03d", i)
		if _, err := sqlDB.Exec(`INSERT INTO envoys(id, hostname) VALUES(?, ?)`, id, host); err != nil {
			t.Fatalf("insert envoy: %v", err)
		}
		if _, err := sqlDB.Exec(`INSERT INTO envoy_traits(envoy_id, traits) VALUES(?, ?)`,
			id, `{"os":{"distro":"ubuntu","version":"22.04","arch":"amd64"}}`); err != nil {
			t.Fatalf("insert traits: %v", err)
		}
	}

	h := &Handler{queries: db.New(sqlDB)}

	// First page: under the cap → emits the infinite-scroll sentinel carrying the
	// running loaded count so the next page can keep counting.
	rec := httptest.NewRecorder()
	h.InventoryFragment(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first page status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-trigger="revealed"`) {
		t.Errorf("first page missing infinite-scroll sentinel")
	}
	if !strings.Contains(body, "loaded=200") {
		t.Errorf("first page sentinel missing running loaded=200 count")
	}

	// Past the cap: with loaded already near the ceiling, the next page must stop
	// scrolling (no sentinel) and show the cap notice instead.
	rec = httptest.NewRecorder()
	h.InventoryFragment(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/inventory?loaded=900", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capped page status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if strings.Contains(body, `hx-trigger="revealed"`) {
		t.Errorf("past the cap the sentinel must not be emitted")
	}
	if !strings.Contains(body, "Showing the first 1100 envoys") {
		t.Errorf("past the cap the stop notice is missing")
	}
}
