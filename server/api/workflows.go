// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/workflows"
)

// WorkflowHandler serves REST endpoints for workflow execution and status.
type WorkflowHandler struct {
	queries     *db.Queries
	engine      *workflows.Engine
	auditWriter auditRecorder
}

// NewWorkflowHandler creates a WorkflowHandler.
func NewWorkflowHandler(queries *db.Queries, engine *workflows.Engine) *WorkflowHandler {
	return &WorkflowHandler{queries: queries, engine: engine}
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *WorkflowHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// RegisterRoutes registers workflow endpoints on the given mux.
// Run + abort require workflow.execute (admin-only by default);
// list / get are fleet.read.
func (h *WorkflowHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	exec := auth.RequirePermission(auth.PermWorkflowExecute)
	mux.Handle("POST /api/v1/workflows", exec(http.HandlerFunc(h.HandleRun)))
	mux.Handle("GET /api/v1/workflows", read(http.HandlerFunc(h.HandleList)))
	mux.Handle("GET /api/v1/workflows/{id}", read(http.HandlerFunc(h.HandleGet)))
	mux.Handle("POST /api/v1/workflows/{id}/abort", exec(http.HandlerFunc(h.HandleAbort)))
}

type workflowRunRequest struct {
	Name string `json:"name"`
}

// HandleRun starts a new workflow execution.
func (h *WorkflowHandler) HandleRun(w http.ResponseWriter, r *http.Request) {
	var req workflowRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// The operator is the authenticated caller, never a client-supplied
	// field — otherwise an authenticated admin could forge the actor on the
	// hash chain and in workflow_runs.operator.
	operator := actorFromRequest(r)

	runID, err := h.engine.RunWorkflow(r.Context(), req.Name, operator)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Record tamper-evident audit event — every workflow start is on
	// the hash chain so a "who ran what when" reconstruction is durable.
	recordAudit(r.Context(), h.auditWriter, "workflow.dispatch", operator, "rest",
		req.Name, "")

	writeJSON(w, r, http.StatusCreated, map[string]string{"id": runID, "status": "running"})
}

type workflowSummary struct {
	ID           string `json:"id"`
	WorkflowName string `json:"workflowName"`
	Status       string `json:"status"`
	CurrentStep  int64  `json:"currentStep"`
	TotalSteps   int64  `json:"totalSteps"`
	Operator     string `json:"operator"`
	CreatedAt    string `json:"createdAt"`
	FinishedAt   string `json:"finishedAt,omitempty"`
}

// HandleList returns recent workflow runs.
func (h *WorkflowHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit := int64(20)
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.ParseInt(l, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	runs, err := h.queries.ListWorkflowRuns(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	summaries := make([]workflowSummary, 0, len(runs))
	for _, run := range runs {
		s := workflowSummary{
			ID:           run.ID,
			WorkflowName: run.WorkflowName,
			Status:       run.Status,
			CurrentStep:  run.CurrentStep,
			TotalSteps:   run.TotalSteps,
			Operator:     run.Operator,
			CreatedAt:    run.CreatedAt.Format(time.RFC3339),
		}
		if run.FinishedAt.Valid {
			s.FinishedAt = run.FinishedAt.Time.Format(time.RFC3339)
		}
		summaries = append(summaries, s)
	}

	writeJSON(w, r, http.StatusOK, map[string]any{"workflows": summaries})
}

type workflowDetail struct {
	ID            string `json:"id"`
	WorkflowName  string `json:"workflowName"`
	Status        string `json:"status"`
	CurrentStep   int64  `json:"currentStep"`
	TotalSteps    int64  `json:"totalSteps"`
	Operator      string `json:"operator"`
	CreatedAt     string `json:"createdAt"`
	StartedAt     string `json:"startedAt,omitempty"`
	FinishedAt    string `json:"finishedAt,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
}

type stepDetail struct {
	ID         string `json:"id"`
	StepIndex  int64  `json:"stepIndex"`
	StepName   string `json:"stepName"`
	TaskRunID  string `json:"taskRunId,omitempty"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// HandleGet returns a workflow run with per-step details.
func (h *WorkflowHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	run, err := h.queries.GetWorkflowRun(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	steps, err := h.queries.ListWorkflowStepResults(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	wd := workflowDetail{
		ID:           run.ID,
		WorkflowName: run.WorkflowName,
		Status:       run.Status,
		CurrentStep:  run.CurrentStep,
		TotalSteps:   run.TotalSteps,
		Operator:     run.Operator,
		CreatedAt:    run.CreatedAt.Format(time.RFC3339),
	}
	if run.StartedAt.Valid {
		wd.StartedAt = run.StartedAt.Time.Format(time.RFC3339)
	}
	if run.FinishedAt.Valid {
		wd.FinishedAt = run.FinishedAt.Time.Format(time.RFC3339)
	}
	if run.FailureReason.Valid {
		wd.FailureReason = run.FailureReason.String
	}

	sds := make([]stepDetail, 0, len(steps))
	for _, s := range steps {
		sd := stepDetail{
			ID:        s.ID,
			StepIndex: s.StepIndex,
			StepName:  s.StepName,
			Status:    s.Status,
		}
		if s.TaskRunID.Valid {
			sd.TaskRunID = s.TaskRunID.String
		}
		if s.StartedAt.Valid {
			sd.StartedAt = s.StartedAt.Time.Format(time.RFC3339)
		}
		if s.FinishedAt.Valid {
			sd.FinishedAt = s.FinishedAt.Time.Format(time.RFC3339)
		}
		sds = append(sds, sd)
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"workflow": wd,
		"steps":    sds,
	})
}

// HandleAbort aborts a running workflow.
func (h *WorkflowHandler) HandleAbort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Verify the workflow exists and is running.
	run, err := h.queries.GetWorkflowRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if run.Status != "running" && run.Status != "pending" {
		writeError(w, http.StatusConflict, "workflow is not running")
		return
	}

	h.engine.Abort(id)

	recordAudit(r.Context(), h.auditWriter, "workflow.abort", actorFromRequest(r), "rest",
		"aborted workflow "+id, "")

	writeJSON(w, r, http.StatusOK, map[string]string{"status": "aborting"})
}
