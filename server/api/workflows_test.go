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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/workflows"
)

// stubRunner is a no-op TaskRunner for workflow tests that start a real
// async run but assert only on the synchronous handler behavior. It lets
// the background executeWorkflow goroutine dispatch cleanly instead of
// nil-dereferencing a nil runner.
type stubRunner struct{}

func (stubRunner) DispatchPush(ctx context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error) {
	return &isgrpc.DispatchResult{ID: "stub-task-run"}, nil
}

// openWorkflowTestDB returns an in-memory SQLite database with the full
// migration schema. Delegates to the shared openMigratedTestDB helper.
func openWorkflowTestDB(t *testing.T) *sql.DB {
	return openMigratedTestDB(t)
}

func TestWorkflowHandler_List(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	queries := db.New(sqlDB)
	engine := workflows.NewEngine(queries, nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(queries, engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Insert runs directly.
	for i := 0; i < 3; i++ {
		_, err := sqlDB.Exec(
			`INSERT INTO workflow_runs (id, workflow_name, operator, status, total_steps) VALUES (?, ?, ?, ?, ?)`,
			uuid.New().String(), fmt.Sprintf("wf-%d", i), "admin", "complete", 2,
		)
		if err != nil {
			t.Fatalf("insert workflow run: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/workflows?limit=2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Workflows []workflowSummary `json:"workflows"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Workflows) != 2 {
		t.Errorf("expected 2 workflows, got %d", len(resp.Workflows))
	}
}

func TestWorkflowHandler_ListEmpty(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	engine := workflows.NewEngine(db.New(sqlDB), nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(db.New(sqlDB), engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/workflows", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestWorkflowHandler_Get(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	queries := db.New(sqlDB)
	engine := workflows.NewEngine(queries, nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(queries, engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	runID := uuid.New().String()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := sqlDB.Exec(
		`INSERT INTO workflow_runs (id, workflow_name, operator, status, total_steps, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, "deploy", "admin", "running", 3, now,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err = sqlDB.Exec(
		`INSERT INTO workflow_step_results (id, workflow_run_id, step_index, step_name, status) VALUES (?, ?, ?, ?, ?)`,
		uuid.New().String(), runID, 0, "setup", "complete",
	)
	if err != nil {
		t.Fatalf("insert step: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/workflows/"+runID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Workflow workflowDetail `json:"workflow"`
		Steps    []stepDetail   `json:"steps"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Workflow.ID != runID {
		t.Errorf("expected id %s, got %s", runID, resp.Workflow.ID)
	}
	if resp.Workflow.WorkflowName != "deploy" {
		t.Errorf("expected name deploy, got %s", resp.Workflow.WorkflowName)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(resp.Steps))
	}
	if resp.Steps[0].StepName != "setup" {
		t.Errorf("expected step name setup, got %s", resp.Steps[0].StepName)
	}
}

func TestWorkflowHandler_GetNotFound(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	engine := workflows.NewEngine(db.New(sqlDB), nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(db.New(sqlDB), engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/workflows/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestWorkflowHandler_RunMissingName(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	engine := workflows.NewEngine(db.New(sqlDB), nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(db.New(sqlDB), engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"operator":"admin"}`
	req := httptest.NewRequest("POST", "/api/v1/workflows", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestWorkflowHandler_RunUnknownWorkflow(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	engine := workflows.NewEngine(db.New(sqlDB), nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(db.New(sqlDB), engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"name":"nonexistent","operator":"admin"}`
	req := httptest.NewRequest("POST", "/api/v1/workflows", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWorkflowHandler_AbortNotFound(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	engine := workflows.NewEngine(db.New(sqlDB), nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(db.New(sqlDB), engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/workflows/nonexistent/abort", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// TestWorkflowHandler_RunRecordsAuditEvent pins workflow.dispatch on
// the audit hash chain: every workflow start carries an audit row with
// the operator + workflow name, mirroring task.dispatch's coverage so
// "who ran what when" is durable for workflows too. Without the
// SetAuditWriter wiring (server/server.go), the engine.RunWorkflow
// success silently lost the only attribution point on the chain.
func TestWorkflowHandler_RunRecordsAuditEvent(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	queries := db.New(sqlDB)
	engine := workflows.NewEngine(queries, stubRunner{}, nil)
	t.Cleanup(engine.Close)

	// Load a workflow from disk so the engine has a known def to start.
	// The dispatch is a background goroutine; we assert only the audit
	// row is written synchronously inside HandleRun.
	dir := t.TempDir()
	wf := `name: trivial
steps:
  - name: noop
    command: "true"
    target: "*"
`
	if err := os.WriteFile(filepath.Join(dir, "trivial.vgo"), []byte(wf), 0644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := engine.Load(dir); err != nil {
		t.Fatalf("load workflows: %v", err)
	}

	audit := &recordingAuditWriter{}
	handler := NewWorkflowHandler(queries, engine)
	handler.SetAuditWriter(audit)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// The body carries a forged operator; the handler must ignore it and
	// attribute to the authenticated caller (adminReq stamps "admin").
	body := `{"name":"trivial","operator":"alice"}`
	req := httptest.NewRequest("POST", "/api/v1/workflows", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(audit.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d: %+v", len(audit.events), audit.events)
	}
	ev := audit.events[0]
	if ev.EventType != "workflow.dispatch" {
		t.Errorf("expected workflow.dispatch event, got %q", ev.EventType)
	}
	// Actor is the authenticated caller, never the forged body operator.
	if ev.Actor != "admin" {
		t.Errorf("expected actor=admin (authenticated caller), got %q", ev.Actor)
	}
	if ev.Summary != "trivial" {
		t.Errorf("expected summary=trivial, got %q", ev.Summary)
	}
}

func TestWorkflowHandler_AbortNotRunning(t *testing.T) {
	sqlDB := openWorkflowTestDB(t)
	queries := db.New(sqlDB)
	engine := workflows.NewEngine(queries, nil, nil)
	t.Cleanup(engine.Close)
	handler := NewWorkflowHandler(queries, engine)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	runID := uuid.New().String()
	sqlDB.Exec(
		`INSERT INTO workflow_runs (id, workflow_name, operator, status, total_steps) VALUES (?, ?, ?, ?, ?)`,
		runID, "deploy", "admin", "complete", 2,
	)

	req := httptest.NewRequest("POST", "/api/v1/workflows/"+runID+"/abort", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}
