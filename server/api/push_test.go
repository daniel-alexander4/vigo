// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/db"
	isgrpc "github.com/daniel-alexander4/vigo/server/grpc"
)

// openPushTestDB creates an in-memory SQLite database with push_runs and push_run_targets tables.
func openPushTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE push_runs (
			id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6)))),
			command        TEXT NOT NULL,
			target_glob    TEXT NOT NULL,
			operator       TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT 'pending',
			total_targets  INTEGER NOT NULL DEFAULT 0,
			created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
			started_at     DATETIME,
			finished_at    DATETIME,
			timeout_secs   INTEGER NOT NULL DEFAULT 60,
			batch_size     TEXT,
			max_failures   TEXT
		);
		CREATE TABLE push_run_targets (
			id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6)))),
			push_run_id    TEXT NOT NULL REFERENCES push_runs (id) ON DELETE CASCADE,
			envoy_id      TEXT NOT NULL,
			hostname       TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'pending',
			exit_code      INTEGER,
			stdout         TEXT,
			stderr         TEXT,
			duration_ms    INTEGER,
			error_msg      TEXT,
			dispatched_at  DATETIME,
			finished_at    DATETIME
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB
}

// stubDispatcher records dispatch calls and returns a fixed result.
type stubDispatcher struct {
	lastParams   isgrpc.DispatchParams
	returnResult *isgrpc.DispatchResult
	returnErr    error
}

func (s *stubDispatcher) DispatchPush(_ context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error) {
	s.lastParams = params
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return s.returnResult, nil
}

func TestPushHandler_Dispatch(t *testing.T) {
	sqlDB := openPushTestDB(t)
	dispatcher := &stubDispatcher{returnResult: &isgrpc.DispatchResult{ID: "push-123", Status: "running"}}
	handler := NewPushHandler(db.New(sqlDB), dispatcher, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime","target":"*.web.*","operator":"admin"}`
	req := httptest.NewRequest("POST", "/api/v1/push", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp isgrpc.DispatchResult
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.ID != "push-123" {
		t.Errorf("expected id push-123, got %s", resp.ID)
	}
	if dispatcher.lastParams.Command != "uptime" {
		t.Errorf("expected command uptime, got %s", dispatcher.lastParams.Command)
	}
	if dispatcher.lastParams.TargetSpec != "*.web.*" {
		t.Errorf("expected target *.web.*, got %s", dispatcher.lastParams.TargetSpec)
	}
}

func TestPushHandler_DispatchDryRun(t *testing.T) {
	sqlDB := openPushTestDB(t)
	dispatcher := &stubDispatcher{returnResult: &isgrpc.DispatchResult{
		Status: "dry_run",
		Batches: []isgrpc.DryRunBatch{
			{Index: 1, Hostnames: []string{"web01", "web02"}},
			{Index: 2, Hostnames: []string{"web03"}},
		},
		Offline: []string{"web04"},
	}}
	handler := NewPushHandler(db.New(sqlDB), dispatcher, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime","target":"*.web.*","batchSize":"2","dryRun":true}`
	req := httptest.NewRequest("POST", "/api/v1/push", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for dry_run, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp isgrpc.DispatchResult
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "dry_run" {
		t.Errorf("expected dry_run status, got %s", resp.Status)
	}
	if len(resp.Batches) != 2 {
		t.Errorf("expected 2 batches, got %d", len(resp.Batches))
	}
	if len(resp.Offline) != 1 || resp.Offline[0] != "web04" {
		t.Errorf("expected offline [web04], got %v", resp.Offline)
	}
	if !dispatcher.lastParams.DryRun {
		t.Error("expected DryRun=true in params")
	}
	if dispatcher.lastParams.BatchSize != "2" {
		t.Errorf("expected batchSize 2, got %s", dispatcher.lastParams.BatchSize)
	}
}

func TestPushHandler_DispatchMissingCommand(t *testing.T) {
	sqlDB := openPushTestDB(t)
	handler := NewPushHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"target":"*.web.*"}`
	req := httptest.NewRequest("POST", "/api/v1/push", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPushHandler_DispatchMissingTarget(t *testing.T) {
	sqlDB := openPushTestDB(t)
	handler := NewPushHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime"}`
	req := httptest.NewRequest("POST", "/api/v1/push", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPushHandler_DispatchError(t *testing.T) {
	sqlDB := openPushTestDB(t)
	dispatcher := &stubDispatcher{returnErr: fmt.Errorf("no envoys matched")}
	handler := NewPushHandler(db.New(sqlDB), dispatcher, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime","target":"nonexistent"}`
	req := httptest.NewRequest("POST", "/api/v1/push", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestPushHandler_List(t *testing.T) {
	sqlDB := openPushTestDB(t)
	queries := db.New(sqlDB)
	handler := NewPushHandler(queries, &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Insert task runs directly.
	for i := 0; i < 3; i++ {
		_, err := sqlDB.Exec(
			`INSERT INTO push_runs (id, command, target_glob, operator, status, total_targets) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), fmt.Sprintf("cmd-%d", i), "*.web.*", "admin", "complete", 2,
		)
		if err != nil {
			t.Fatalf("insert push run: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/push?limit=2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		PushRuns []pushSummary `json:"pushRuns"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.PushRuns) != 2 {
		t.Errorf("expected 2 push runs, got %d", len(resp.PushRuns))
	}
}

func TestPushHandler_ListEmpty(t *testing.T) {
	sqlDB := openPushTestDB(t)
	handler := NewPushHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/push", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		PushRuns []pushSummary `json:"pushRuns"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.PushRuns) != 0 {
		t.Errorf("expected 0 push runs, got %d", len(resp.PushRuns))
	}
}

func TestPushHandler_Get(t *testing.T) {
	sqlDB := openPushTestDB(t)
	handler := NewPushHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	pushRunID := uuid.New().String()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := sqlDB.Exec(
		`INSERT INTO push_runs (id, command, target_glob, operator, status, total_targets, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pushRunID, "uptime", "*.web.*", "admin", "complete", 2, now,
	)
	if err != nil {
		t.Fatalf("insert push run: %v", err)
	}

	targetID := uuid.New().String()
	_, err = sqlDB.Exec(
		`INSERT INTO push_run_targets (id, push_run_id, envoy_id, hostname, status, exit_code, stdout, duration_ms, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		targetID, pushRunID, uuid.New().String(), "web01.prod", "complete", 0, " 10:30:00 up 5 days\n", 42, now,
	)
	if err != nil {
		t.Fatalf("insert target: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/push/"+pushRunID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Push    pushDetail     `json:"push"`
		Targets []targetDetail `json:"targets"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Push.ID != pushRunID {
		t.Errorf("expected push run ID %s, got %s", pushRunID, resp.Push.ID)
	}
	if resp.Push.Command != "uptime" {
		t.Errorf("expected command uptime, got %s", resp.Push.Command)
	}
	if resp.Push.Status != "complete" {
		t.Errorf("expected status complete, got %s", resp.Push.Status)
	}
	if len(resp.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(resp.Targets))
	}
	if resp.Targets[0].Hostname != "web01.prod" {
		t.Errorf("expected hostname web01.prod, got %s", resp.Targets[0].Hostname)
	}
	if resp.Targets[0].ExitCode == nil || *resp.Targets[0].ExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", resp.Targets[0].ExitCode)
	}
	if resp.Targets[0].Stdout != " 10:30:00 up 5 days\n" {
		t.Errorf("unexpected stdout: %q", resp.Targets[0].Stdout)
	}
}

func TestPushHandler_GetNotFound(t *testing.T) {
	sqlDB := openPushTestDB(t)
	handler := NewPushHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/push/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestPushHandler_RequireDefinitionBlocksAdHoc(t *testing.T) {
	sqlDB := openPushTestDB(t)
	handler := NewPushHandler(db.New(sqlDB), &stubDispatcher{}, true) // require_definition enabled
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Ad-hoc command (no taskName) should be rejected.
	body := `{"command":"echo hello","target":"*"}`
	req := httptest.NewRequest("POST", "/api/v1/push", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for ad-hoc command, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPushHandler_RequireDefinitionAllowsNamed(t *testing.T) {
	sqlDB := openPushTestDB(t)
	dispatcher := &stubDispatcher{returnResult: &isgrpc.DispatchResult{ID: "push-456", Status: "running"}}
	handler := NewPushHandler(db.New(sqlDB), dispatcher, true) // require_definition enabled
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Named task (taskName set) should be allowed.
	body := `{"taskName":"restart-service","target":"*","params":{"service":"nginx"}}`
	req := httptest.NewRequest("POST", "/api/v1/push", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("expected 200/201 for named push, got %d: %s", rec.Code, rec.Body.String())
	}
}
