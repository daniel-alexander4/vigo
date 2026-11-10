// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
	"git.alexander4.io/vigo/server/validate"
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

// recordedAuditEvent captures a single recordAudit call for assertions.
type recordedAuditEvent struct {
	EventType string
	Actor     string
	Source    string
	Summary   string
	Payload   string
}

// recordingAuditWriter implements auditRecorder and stores every call.
type recordingAuditWriter struct {
	events []recordedAuditEvent
}

func (w *recordingAuditWriter) Record(_ context.Context, eventType, actor, source, summary, payload string) error {
	w.events = append(w.events, recordedAuditEvent{eventType, actor, source, summary, payload})
	return nil
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
			`INSERT INTO task_runs (id, command, target_glob, operator, status, total_targets) VALUES (?, ?, ?, ?, ?, ?)`,
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

// insertTaskRun seeds one task_runs row with the given status and returns its id.
func insertTaskRun(t *testing.T, sqlDB *sql.DB, status string) string {
	t.Helper()
	id := uuid.New().String()
	if _, err := sqlDB.Exec(
		`INSERT INTO task_runs (id, command, target_glob, operator, status, total_targets) VALUES (?, ?, ?, ?, ?, ?)`,
		id, "cmd", "*", "admin", status, 1); err != nil {
		t.Fatalf("insert task run: %v", err)
	}
	return id
}

// TestTaskHandler_CancelUnknown_NotFound pins that cancelling an unknown task
// id returns 404 — it used to silently "succeed" with 200 (the guarded UPDATE
// affects 0 rows for an unknown id without erroring).
func TestTaskHandler_CancelUnknown_NotFound(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/task/does-not-exist/cancel", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown id: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTaskHandler_CancelFinished_Conflict pins that cancelling an
// already-finished task returns 409, not a silent 200.
func TestTaskHandler_CancelFinished_Conflict(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	id := insertTaskRun(t, sqlDB, "complete")
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/task/"+id+"/cancel", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel finished task: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTaskHandler_CancelRunning_OK confirms the happy path still cancels.
func TestTaskHandler_CancelRunning_OK(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	id := insertTaskRun(t, sqlDB, "running")
	handler := NewTaskHandler(db.New(sqlDB), &stubDispatcher{}, false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/task/"+id+"/cancel", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel running task: expected 200, got %d: %s", rec.Code, rec.Body.String())
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
		`INSERT INTO task_runs (id, command, target_glob, operator, status, total_targets, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
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
		`INSERT INTO task_run_targets (id, task_run_id, envoy_id, hostname, status, exit_code, stdout, duration_ms, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
	body := `{"command":"longdrawer hello","target":"*"}`
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

// TestTaskHandler_DispatchBlockedByGuardrail pins the 400-shaped
// response when ValidatePushCommand rejects a dispatch: structured
// body with blocked=true + reason + pattern (so the CLI can prompt and
// retry with --force) and a `task.blocked` audit entry on the hash
// chain. A bare 500 would lose both — operator sees an opaque error
// and audit has no record of the refused dispatch.
func TestTaskHandler_DispatchBlockedByGuardrail(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	dispatcher := &stubDispatcher{
		returnErr: &validate.Violation{Reason: "command matches dangerous pattern", Pattern: "systemctl stop vigo"},
	}
	audit := &recordingAuditWriter{}
	handler := NewTaskHandler(db.New(sqlDB), dispatcher, false)
	handler.SetAuditWriter(audit)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// The body carries a forged operator; the handler must ignore it and
	// attribute to the authenticated caller (adminReq stamps "admin").
	body := `{"command":"systemctl stop vigo-envoy","target":"*","operator":"alice"}`
	req := httptest.NewRequest("POST", "/api/v1/task", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp blockedResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Blocked {
		t.Errorf("expected blocked=true, got %+v", resp)
	}
	if resp.Pattern != "systemctl stop vigo" {
		t.Errorf("expected pattern in response, got %q", resp.Pattern)
	}
	if resp.Reason != "command matches dangerous pattern" {
		t.Errorf("expected reason in response, got %q", resp.Reason)
	}
	if len(audit.events) != 1 || audit.events[0].EventType != "task.blocked" {
		t.Fatalf("expected single task.blocked audit event, got %+v", audit.events)
	}
	if !strings.Contains(audit.events[0].Payload, "systemctl stop vigo") {
		t.Errorf("expected payload to carry matched pattern, got %q", audit.events[0].Payload)
	}
	// Actor is the authenticated caller, never the forged body operator.
	if audit.events[0].Actor != "admin" {
		t.Errorf("expected actor=admin (authenticated caller), got %q", audit.events[0].Actor)
	}
}

// TestTaskHandler_DispatchForceBypassesGuardrail pins force=true:
// DispatchParams.Force is passed through, the audit event is
// `task.dispatch` with a `forced` payload (so audit reviewers can find
// every bypass), and the response is the normal 201 dispatch result.
func TestTaskHandler_DispatchForceBypassesGuardrail(t *testing.T) {
	sqlDB := openTaskTestDB(t)
	dispatcher := &stubDispatcher{returnResult: &isgrpc.DispatchResult{ID: "push-forced", Status: "running"}}
	audit := &recordingAuditWriter{}
	handler := NewTaskHandler(db.New(sqlDB), dispatcher, false)
	handler.SetAuditWriter(audit)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"command":"systemctl stop vigo-envoy","target":"*","operator":"alice","force":true}`
	req := httptest.NewRequest("POST", "/api/v1/task", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !dispatcher.lastParams.Force {
		t.Error("expected Force=true to reach DispatchPush")
	}
	if len(audit.events) != 1 {
		t.Fatalf("expected one audit event, got %d: %+v", len(audit.events), audit.events)
	}
	ev := audit.events[0]
	if ev.EventType != "task.dispatch" {
		t.Errorf("expected task.dispatch (with forced payload), got %q", ev.EventType)
	}
	if !strings.Contains(ev.Payload, `"forced":true`) {
		t.Errorf("expected forced=true payload, got %q", ev.Payload)
	}
}
