// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hobnob/hobnob/server/db"
	isgrpc "github.com/hobnob/hobnob/server/grpc"
)

// TaskDispatcher dispatches ad-hoc commands to connected agents.
type TaskDispatcher interface {
	DispatchTask(ctx context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error)
}

// TasksHandler serves the REST endpoints for ad-hoc task dispatch and status.
type TasksHandler struct {
	queries     *db.Queries
	dispatcher  TaskDispatcher
	auditWriter auditRecorder
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *TasksHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// NewTasksHandler creates a TasksHandler with database access and task dispatch.
func NewTasksHandler(queries *db.Queries, dispatcher TaskDispatcher) *TasksHandler {
	return &TasksHandler{queries: queries, dispatcher: dispatcher}
}

// RegisterRoutes registers task endpoints on the given mux.
func (h *TasksHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tasks", h.HandleDispatch)
	mux.HandleFunc("GET /api/v1/tasks", h.HandleList)
	mux.HandleFunc("GET /api/v1/tasks/{id}", h.HandleGet)
}

type dispatchRequest struct {
	Command     string            `json:"command"`
	Target      string            `json:"target"`
	Operator    string            `json:"operator"`
	Timeout     int32             `json:"timeout"`
	BatchSize   string            `json:"batchSize"`
	MaxFailures string            `json:"maxFailures"`
	HealthCheck string            `json:"healthCheck"`
	DryRun      bool              `json:"dryRun"`
	TaskName    string            `json:"taskName"`
	Params      map[string]string `json:"params"`
}

// HandleDispatch creates a new task run and dispatches the command to matching agents.
func (h *TasksHandler) HandleDispatch(w http.ResponseWriter, r *http.Request) {
	var req dispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Command == "" && req.TaskName == "" {
		writeJSONError(w, http.StatusBadRequest, "command or taskName is required")
		return
	}
	if req.Target == "" && req.TaskName == "" {
		writeJSONError(w, http.StatusBadRequest, "target is required")
		return
	}
	if req.Operator == "" {
		req.Operator = "api"
	}

	result, err := h.dispatcher.DispatchTask(r.Context(), isgrpc.DispatchParams{
		Command:     req.Command,
		TargetSpec:  req.Target,
		Operator:    req.Operator,
		TimeoutSecs: req.Timeout,
		BatchSize:   req.BatchSize,
		MaxFailures: req.MaxFailures,
		HealthCheck: req.HealthCheck,
		DryRun:      req.DryRun,
		TaskName:    req.TaskName,
		Params:      req.Params,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Record tamper-evident audit event (skip dry runs).
	if result.Status != "dry_run" {
		recordAudit(r.Context(), h.auditWriter, "task.dispatch", req.Operator,
			req.Command+" on "+req.Target, "")
	}

	w.Header().Set("Content-Type", "application/json")
	if result.Status == "dry_run" {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(result)
}

type taskSummary struct {
	ID           string `json:"id"`
	Command      string `json:"command"`
	Target       string `json:"target"`
	Operator     string `json:"operator"`
	Status       string `json:"status"`
	TotalTargets int64  `json:"totalTargets"`
	CreatedAt    string `json:"createdAt"`
	FinishedAt   string `json:"finishedAt,omitempty"`
}

// HandleList returns recent task runs.
func (h *TasksHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit := int64(20)
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.ParseInt(l, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	tasks, err := h.queries.ListTaskRuns(r.Context(), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	summaries := make([]taskSummary, 0, len(tasks))
	for _, t := range tasks {
		s := taskSummary{
			ID:           t.ID,
			Command:      t.Command,
			Target:       t.TargetGlob,
			Operator:     t.Operator,
			Status:       t.Status,
			TotalTargets: t.TotalTargets,
			CreatedAt:    t.CreatedAt.Format(time.RFC3339),
		}
		if t.FinishedAt.Valid {
			s.FinishedAt = t.FinishedAt.Time.Format(time.RFC3339)
		}
		summaries = append(summaries, s)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"tasks": summaries})
}

type taskDetail struct {
	ID           string `json:"id"`
	Command      string `json:"command"`
	Target       string `json:"target"`
	Operator     string `json:"operator"`
	Status       string `json:"status"`
	TotalTargets int64  `json:"totalTargets"`
	TimeoutSecs  int64  `json:"timeoutSecs"`
	BatchSize    string `json:"batchSize,omitempty"`
	MaxFailures  string `json:"maxFailures,omitempty"`
	CreatedAt    string `json:"createdAt"`
	StartedAt    string `json:"startedAt,omitempty"`
	FinishedAt   string `json:"finishedAt,omitempty"`
}

type targetDetail struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	Status     string `json:"status"`
	ExitCode   *int64 `json:"exitCode,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMs *int64 `json:"durationMs,omitempty"`
	ErrorMsg   string `json:"errorMsg,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// HandleGet returns a task run with per-target details.
func (h *TasksHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	task, err := h.queries.GetTaskRun(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "task not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	targets, err := h.queries.ListTaskRunTargets(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	td := taskDetail{
		ID:           task.ID,
		Command:      task.Command,
		Target:       task.TargetGlob,
		Operator:     task.Operator,
		Status:       task.Status,
		TotalTargets: task.TotalTargets,
		TimeoutSecs:  task.TimeoutSecs,
		CreatedAt:    task.CreatedAt.Format(time.RFC3339),
	}
	if task.BatchSize.Valid {
		td.BatchSize = task.BatchSize.String
	}
	if task.MaxFailures.Valid {
		td.MaxFailures = task.MaxFailures.String
	}
	if task.StartedAt.Valid {
		td.StartedAt = task.StartedAt.Time.Format(time.RFC3339)
	}
	if task.FinishedAt.Valid {
		td.FinishedAt = task.FinishedAt.Time.Format(time.RFC3339)
	}

	tds := make([]targetDetail, 0, len(targets))
	for _, t := range targets {
		d := targetDetail{
			ID:       t.ID,
			Hostname: t.Hostname,
			Status:   t.Status,
		}
		if t.ExitCode.Valid {
			ec := t.ExitCode.Int64
			d.ExitCode = &ec
		}
		if t.Stdout.Valid {
			d.Stdout = t.Stdout.String
		}
		if t.Stderr.Valid {
			d.Stderr = t.Stderr.String
		}
		if t.DurationMs.Valid {
			dm := t.DurationMs.Int64
			d.DurationMs = &dm
		}
		if t.ErrorMsg.Valid {
			d.ErrorMsg = t.ErrorMsg.String
		}
		if t.FinishedAt.Valid {
			d.FinishedAt = t.FinishedAt.Time.Format(time.RFC3339)
		}
		tds = append(tds, d)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"task":    td,
		"targets": tds,
	})
}
