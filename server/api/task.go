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

	"github.com/daniel-alexander4/vigo/server/db"
	isgrpc "github.com/daniel-alexander4/vigo/server/grpc"
)

// TaskDispatcher dispatches ad-hoc commands to connected agents.
type TaskDispatcher interface {
	DispatchPush(ctx context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error)
}

// TaskHandler serves the REST endpoints for ad-hoc task dispatch and status.
type TaskHandler struct {
	queries            *db.Queries
	dispatcher         TaskDispatcher
	auditWriter        auditRecorder
	requireDefinition  bool
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *TaskHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// NewTaskHandler creates a TaskHandler with database access and task dispatch.
func NewTaskHandler(queries *db.Queries, dispatcher TaskDispatcher, requireDefinition bool) *TaskHandler {
	return &TaskHandler{queries: queries, dispatcher: dispatcher, requireDefinition: requireDefinition}
}

// RegisterRoutes registers task dispatch endpoints on the given mux.
func (h *TaskHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/task", h.HandleDispatch)
	mux.HandleFunc("GET /api/v1/task", h.HandleList)
	mux.HandleFunc("GET /api/v1/task/{id}", h.HandleGet)
	mux.HandleFunc("POST /api/v1/task/{id}/cancel", h.HandleCancel)
	mux.HandleFunc("POST /api/v1/task/cancel-running", h.HandleCancelRunning)
}

type dispatchRequest struct {
	Command     string            `json:"command"`
	Target      string            `json:"target"`
	Operator    string            `json:"operator"`
	Timeout     int32             `json:"timeout"`
	Nice        int32             `json:"nice"`
	BatchSize   string            `json:"batchSize"`
	MaxFailures string            `json:"maxFailures"`
	HealthCheck string            `json:"healthCheck"`
	DryRun      bool              `json:"dryRun"`
	TaskName    string            `json:"taskName"`
	Params      map[string]string `json:"params"`
}

// HandleDispatch creates a new task run and dispatches the command to matching agents.
func (h *TaskHandler) HandleDispatch(w http.ResponseWriter, r *http.Request) {
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
	if h.requireDefinition && req.TaskName == "" {
		writeJSONError(w, http.StatusForbidden, "ad-hoc commands are disabled; use a named task definition (task.require_definition is enabled)")
		return
	}
	if req.Operator == "" {
		req.Operator = "api"
	}

	result, err := h.dispatcher.DispatchPush(r.Context(), isgrpc.DispatchParams{
		Command:     req.Command,
		TargetSpec:  req.Target,
		Operator:    req.Operator,
		TimeoutSecs: req.Timeout,
		Nice:        req.Nice,
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
		recordAudit(r.Context(), h.auditWriter, "task.dispatch", req.Operator, "rest",
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
func (h *TaskHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit := int64(20)
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.ParseInt(l, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	taskRuns, err := h.queries.ListPushRuns(r.Context(), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	summaries := make([]taskSummary, 0, len(taskRuns))
	for _, t := range taskRuns {
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
	json.NewEncoder(w).Encode(map[string]any{"taskRuns": summaries})
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
func (h *TaskHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	taskRun, err := h.queries.GetPushRun(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "task run not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	targets, err := h.queries.ListPushRunTargets(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	td := taskDetail{
		ID:           taskRun.ID,
		Command:      taskRun.Command,
		Target:       taskRun.TargetGlob,
		Operator:     taskRun.Operator,
		Status:       taskRun.Status,
		TotalTargets: taskRun.TotalTargets,
		TimeoutSecs:  taskRun.TimeoutSecs,
		CreatedAt:    taskRun.CreatedAt.Format(time.RFC3339),
	}
	if taskRun.BatchSize.Valid {
		td.BatchSize = taskRun.BatchSize.String
	}
	if taskRun.MaxFailures.Valid {
		td.MaxFailures = taskRun.MaxFailures.String
	}
	if taskRun.StartedAt.Valid {
		td.StartedAt = taskRun.StartedAt.Time.Format(time.RFC3339)
	}
	if taskRun.FinishedAt.Valid {
		td.FinishedAt = taskRun.FinishedAt.Time.Format(time.RFC3339)
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

// HandleCancel cancels a specific task run and its pending targets.
func (h *TaskHandler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "task id is required")
		return
	}

	ctx := r.Context()
	if err := h.queries.CancelPushRun(ctx, id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel task: "+err.Error())
		return
	}
	_ = h.queries.CancelPushRunTargets(ctx, id)

	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "id": id})
}

// HandleCancelRunning cancels all tasks stuck in pending/running state.
func (h *TaskHandler) HandleCancelRunning(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get IDs of running tasks so we can cancel their targets too.
	ids, err := h.queries.ListRunningPushRunIDs(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list running tasks: "+err.Error())
		return
	}

	for _, id := range ids {
		_ = h.queries.CancelPushRunTargets(ctx, id)
	}

	if err := h.queries.CancelAllRunningPushRuns(ctx); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel running tasks: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "count": len(ids)})
}
