// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/publish"
)

func TestConfig_GetStatus_Healthy(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	configStore := newDoctorTestConfigStore(t)

	handler := NewConfigHandler(queries, configStore, fleet.NewFleetIndex(), &publish.State{}, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["healthy"] != true {
		t.Errorf("expected healthy=true, got %v", resp["healthy"])
	}
	if got := resp["last_error"]; got != "" {
		t.Errorf("expected last_error=\"\", got %q", got)
	}
	if resp["last_reload_at"] == "" {
		t.Error("expected last_reload_at to be a non-empty RFC3339 timestamp")
	} else if _, err := time.Parse(time.RFC3339, resp["last_reload_at"].(string)); err != nil {
		t.Errorf("last_reload_at not RFC3339: %v", err)
	}
}

func TestConfig_ReloadConfig_IncrementsPublishCounter(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	configStore := newDoctorTestConfigStore(t)

	handler := NewConfigHandler(queries, configStore, fleet.NewFleetIndex(), &publish.State{}, "", "")
	m := metrics.NewTestMetrics()
	handler.SetMetrics(m)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/config/reload", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := testutil.ToFloat64(m.ConfigPublishesTotal); got != 1 {
		t.Errorf("ConfigPublishesTotal = %v, want 1 after one successful reload", got)
	}

	// Second reload — counter should advance to 2.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(httptest.NewRequest("POST", "/api/v1/config/reload", nil)))
	if got := testutil.ToFloat64(m.ConfigPublishesTotal); got != 2 {
		t.Errorf("ConfigPublishesTotal = %v after second reload, want 2", got)
	}
}

// TestConfig_ReloadConfig_TriggersComplianceSnapshot pins the publish-hook
// → snapshot wiring: every successful reload fires the registered
// snapshotter exactly once. The dashboard standards-summary reads from
// compliance_history, so this is how operator changes (new waivers,
// framework toggles, scope shifts) surface without waiting for the
// daily background tick.
func TestConfig_ReloadConfig_TriggersComplianceSnapshot(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	configStore := newDoctorTestConfigStore(t)

	handler := NewConfigHandler(queries, configStore, fleet.NewFleetIndex(), &publish.State{}, "", "")
	var (
		mu     sync.Mutex
		fired  int
		done   = make(chan struct{}, 4)
	)
	handler.SetComplianceSnapshotter(func(ctx context.Context) {
		mu.Lock()
		fired++
		mu.Unlock()
		done <- struct{}{}
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Two successful reloads — expect exactly two snapshot fires.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, adminReq(httptest.NewRequest("POST", "/api/v1/config/reload", nil)))
		if rec.Code != http.StatusOK {
			t.Fatalf("reload %d: expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// Snapshotter runs in a goroutine; wait for both fires before checking.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("snapshotter fire %d did not arrive within 2s", i+1)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if fired != 2 {
		t.Errorf("snapshotter fired %d times after two reloads, want 2", fired)
	}
}

func TestConfig_GetStatus_Unhealthy(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))
	configStore := newUnhealthyConfigStore(t)

	handler := NewConfigHandler(queries, configStore, fleet.NewFleetIndex(), &publish.State{}, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["healthy"] != false {
		t.Errorf("expected healthy=false, got %v", resp["healthy"])
	}
	if got, _ := resp["last_error"].(string); got == "" {
		t.Error("expected non-empty last_error")
	}
}

func TestConfig_RecordPublish(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), &publish.State{}, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"operator":"alice","changed_files":["modules/nginx.vgo"],"removed_files":[],"diff_summary":"updated nginx module"}`
	req := httptest.NewRequest("POST", "/api/v1/config/publishes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)

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

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), &publish.State{}, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/publishes?limit=2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)

	var publishID string
	err := sqlDB.QueryRow(
		`INSERT INTO config_publishes (operator, changed_files, removed_files, diff_summary) VALUES (?, ?, ?, ?) RETURNING id`,
		"alice", `["modules/nginx.vgo"]`, `["modules/old.vgo"]`, "updated nginx",
	).Scan(&publishID)
	if err != nil {
		t.Fatalf("insert publish: %v", err)
	}

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), &publish.State{}, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/publishes/"+publishID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	queries := db.New(openMigratedTestDB(t))

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), &publish.State{}, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/config/publishes/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestConfig_Freeze(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))

	state := &publish.State{}
	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), state, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	until := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"freeze_until":"` + until + `"}`
	req := httptest.NewRequest("POST", "/api/v1/config/freeze", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["freeze_until"] == nil {
		t.Error("expected freeze_until to be set")
	}

	// Verify the handler-scoped freeze is set.
	if !state.Frozen() {
		t.Error("expected publish state to report frozen after Freeze handler")
	}
}

func TestConfig_FreezeInvalidTimestamp(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))

	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), &publish.State{}, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"freeze_until":"not-a-timestamp"}`
	req := httptest.NewRequest("POST", "/api/v1/config/freeze", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestConfig_Unfreeze(t *testing.T) {
	queries := db.New(openMigratedTestDB(t))

	state := &publish.State{}
	handler := NewConfigHandler(queries, nil, fleet.NewFleetIndex(), state, "", "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Set a freeze first.
	state.SetFreezeUntil(time.Now().Add(1 * time.Hour))

	req := httptest.NewRequest("POST", "/api/v1/config/unfreeze", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if state.Frozen() {
		t.Error("expected publish state to report not-frozen after Unfreeze handler")
	}
}
