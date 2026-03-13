// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/workflows"
)

// WorkflowHandler serves REST endpoints for workflow execution and status.
type WorkflowHandler struct {
	queries *db.Queries
	engine  *workflows.Engine
}

// NewWorkflowHandler creates a WorkflowHandler.
func NewWorkflowHandler(queries *db.Queries, engine *workflows.Engine) *WorkflowHandler {
	return &WorkflowHandler{queries: queries, engine: engine}
}

// RegisterRoutes registers workflow endpoints on the given mux.
func (h *WorkflowHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/workflows", h.HandleRun)
	mux.HandleFunc("GET /api/v1/workflows", h.HandleList)
	mux.HandleFunc("GET /api/v1/workflows/{id}", h.HandleGet)
	mux.HandleFunc("POST /api/v1/workflows/{id}/abort", h.HandleAbort)
}

type workflowRunRequest struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
}

// HandleRun starts a new workflow execution.
func (h *WorkflowHandler) HandleRun(w http.ResponseWriter, r *http.Request) {
	var req workflowRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Operator == "" {
		req.Operator = "api"
	}

	runID, err := h.engine.RunWorkflow(r.Context(), req.Name, req.Operator)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": runID, "status": "running"})
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
		writeJSONError(w, http.StatusInternalServerError, "internal error")
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"workflows": summaries})
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
	ID        string `json:"id"`
	StepIndex int64  `json:"stepIndex"`
	StepName  string `json:"stepName"`
	TaskRunID string `json:"taskRunId,omitempty"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// HandleGet returns a workflow run with per-step details.
func (h *WorkflowHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	run, err := h.queries.GetWorkflowRun(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	steps, err := h.queries.ListWorkflowStepResults(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
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
			writeJSONError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if run.Status != "running" && run.Status != "pending" {
		writeJSONError(w, http.StatusConflict, "workflow is not running")
		return
	}

	h.engine.Abort(id)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "aborting"})
}
