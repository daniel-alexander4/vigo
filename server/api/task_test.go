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

	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
)

// openTaskTestDB returns an in-memory SQLite database with the full
// migration schema. Delegates to the shared openMigratedTestDB helper.
func openTaskTestDB(t *testing.T) *sql.DB {
	return openMigratedTestDB(t)
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

func TestTaskHandler_Dispatch(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	dispatcher := &stubDispatcher{returnResult: &isgrpc.DispatchResult{ID: "push-123", Status: "running"}}
	handler := NewTaskHandler(db.New(sqlDB), dispatcher, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime","target":"*.web.*","operator":"admin"}`
	req := httptest.NewRequest("POST", "/api/v1/task", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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

func TestTaskHandler_DispatchDryRun(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	dispatcher := &stubDispatcher{returnResult: &isgrpc.DispatchResult{
		Status: "dry_run",
		Batches: []isgrpc.DryRunBatch{
			{Index: 1, Hostnames: []string{"web01", "web02"}},
			{Index: 2, Hostnames: []string{"web03"}},
		},
		Offline: []string{"web04"},
	}}
	handler := NewTaskHandler(db.New(sqlDB), dispatcher, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime","target":"*.web.*","batchSize":"2","dryRun":true}`
	req := httptest.NewRequest("POST", "/api/v1/task", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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

func TestTaskHandler_DispatchMissingCommand(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"target":"*.web.*"}`
	req := httptest.NewRequest("POST", "/api/v1/task", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestTaskHandler_DispatchMissingTarget(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime"}`
	req := httptest.NewRequest("POST", "/api/v1/task", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestTaskHandler_DispatchError(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	dispatcher := &stubDispatcher{returnErr: fmt.Errorf("no envoys matched")}
	handler := NewTaskHandler(db.New(sqlDB), dispatcher, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"uptime","target":"nonexistent"}`
	req := httptest.NewRequest("POST", "/api/v1/task", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestTaskHandler_List(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	queries := db.New(sqlDB)
	handler := NewTaskHandler(queries, &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Insert task runs directly.
	for i := 0; i < 3; i++ {
		_, err := sqlDB.Exec(
			`INSERT INTO push_runs (id, command, target_glob, operator, status, total_targets) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.New().String(), fmt.Sprintf("cmd-%d", i), "*.web.*", "admin", "complete", 2,
		)
		if err != nil {
			t.Fatalf("insert task run: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/task?limit=2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		TaskRuns []taskSummary `json:"taskRuns"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.TaskRuns) != 2 {
		t.Errorf("expected 2 task runs, got %d", len(resp.TaskRuns))
	}
}

func TestTaskHandler_ListEmpty(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/task", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		TaskRuns []taskSummary `json:"taskRuns"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.TaskRuns) != 0 {
		t.Errorf("expected 0 task runs, got %d", len(resp.TaskRuns))
	}
}

func TestTaskHandler_Get(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	taskRunID := uuid.New().String()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := sqlDB.Exec(
		`INSERT INTO push_runs (id, command, target_glob, operator, status, total_targets, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		taskRunID, "uptime", "*.web.*", "admin", "complete", 2, now,
	)
	if err != nil {
		t.Fatalf("insert task run: %v", err)
	}

	envoyID := uuid.New().String()
	_, err = sqlDB.Exec(
		`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES (?, ?, ?, ?)`,
		envoyID, "web01.prod", "10.0.0.1", "key",
	)
	if err != nil {
		t.Fatalf("insert envoy: %v", err)
	}

	targetID := uuid.New().String()
	_, err = sqlDB.Exec(
		`INSERT INTO push_run_targets (id, push_run_id, envoy_id, hostname, status, exit_code, stdout, duration_ms, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		targetID, taskRunID, envoyID, "web01.prod", "complete", 0, " 10:30:00 up 5 days\n", 42, now,
	)
	if err != nil {
		t.Fatalf("insert target: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/task/"+taskRunID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Task    taskDetail     `json:"task"`
		Targets []targetDetail `json:"targets"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Task.ID != taskRunID {
		t.Errorf("expected task run ID %s, got %s", taskRunID, resp.Task.ID)
	}
	if resp.Task.Command != "uptime" {
		t.Errorf("expected command uptime, got %s", resp.Task.Command)
	}
	if resp.Task.Status != "complete" {
		t.Errorf("expected status complete, got %s", resp.Task.Status)
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

func TestTaskHandler_GetNotFound(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/task/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestTaskHandler_RequireDefinitionBlocksAdHoc(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, true) // require_definition enabled
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Ad-hoc command (no taskName) should be rejected.
	body := `{"command":"filesync hello","target":"*"}`
	req := httptest.NewRequest("POST", "/api/v1/task", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for ad-hoc command, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskHandler_RequireDefinitionAllowsNamed(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	dispatcher := &stubDispatcher{returnResult: &isgrpc.DispatchResult{ID: "push-456", Status: "running"}}
	handler := NewTaskHandler(db.New(sqlDB), dispatcher, true) // require_definition enabled
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Named task (taskName set) should be allowed.
	body := `{"taskName":"restart-service","target":"*","params":{"service":"nginx"}}`
	req := httptest.NewRequest("POST", "/api/v1/task", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("expected 200/201 for named task, got %d: %s", rec.Code, rec.Body.String())
	}
}
