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

// PushDispatcher dispatches ad-hoc commands to connected agents.
type PushDispatcher interface {
	DispatchPush(ctx context.Context, params isgrpc.DispatchParams) (*isgrpc.DispatchResult, error)
}

// PushHandler serves the REST endpoints for ad-hoc push dispatch and status.
type PushHandler struct {
	queries            *db.Queries
	dispatcher         PushDispatcher
	auditWriter        auditRecorder
	requireDefinition  bool
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *PushHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// NewPushHandler creates a PushHandler with database access and push dispatch.
func NewPushHandler(queries *db.Queries, dispatcher PushDispatcher, requireDefinition bool) *PushHandler {
	return &PushHandler{queries: queries, dispatcher: dispatcher, requireDefinition: requireDefinition}
}

// RegisterRoutes registers push endpoints on the given mux.
func (h *PushHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/push", h.HandleDispatch)
	mux.HandleFunc("GET /api/v1/push", h.HandleList)
	mux.HandleFunc("GET /api/v1/push/{id}", h.HandleGet)
	mux.HandleFunc("POST /api/v1/push/{id}/cancel", h.HandleCancel)
	mux.HandleFunc("POST /api/v1/push/cancel-running", h.HandleCancelRunning)
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

// HandleDispatch creates a new push run and dispatches the command to matching agents.
func (h *PushHandler) HandleDispatch(w http.ResponseWriter, r *http.Request) {
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
		writeJSONError(w, http.StatusForbidden, "ad-hoc commands are disabled; use a named push definition (push.require_definition is enabled)")
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
		recordAudit(r.Context(), h.auditWriter, "push.dispatch", req.Operator,
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

type pushSummary struct {
	ID           string `json:"id"`
	Command      string `json:"command"`
	Target       string `json:"target"`
	Operator     string `json:"operator"`
	Status       string `json:"status"`
	TotalTargets int64  `json:"totalTargets"`
	CreatedAt    string `json:"createdAt"`
	FinishedAt   string `json:"finishedAt,omitempty"`
}

// HandleList returns recent push runs.
func (h *PushHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit := int64(20)
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.ParseInt(l, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	pushRuns, err := h.queries.ListPushRuns(r.Context(), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	summaries := make([]pushSummary, 0, len(pushRuns))
	for _, t := range pushRuns {
		s := pushSummary{
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
	json.NewEncoder(w).Encode(map[string]any{"pushRuns": summaries})
}

type pushDetail struct {
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

// HandleGet returns a push run with per-target details.
func (h *PushHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	pushRun, err := h.queries.GetPushRun(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "push run not found")
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

	pd := pushDetail{
		ID:           pushRun.ID,
		Command:      pushRun.Command,
		Target:       pushRun.TargetGlob,
		Operator:     pushRun.Operator,
		Status:       pushRun.Status,
		TotalTargets: pushRun.TotalTargets,
		TimeoutSecs:  pushRun.TimeoutSecs,
		CreatedAt:    pushRun.CreatedAt.Format(time.RFC3339),
	}
	if pushRun.BatchSize.Valid {
		pd.BatchSize = pushRun.BatchSize.String
	}
	if pushRun.MaxFailures.Valid {
		pd.MaxFailures = pushRun.MaxFailures.String
	}
	if pushRun.StartedAt.Valid {
		pd.StartedAt = pushRun.StartedAt.Time.Format(time.RFC3339)
	}
	if pushRun.FinishedAt.Valid {
		pd.FinishedAt = pushRun.FinishedAt.Time.Format(time.RFC3339)
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
		"push":    pd,
		"targets": tds,
	})
}

// HandleCancel cancels a specific push run and its pending targets.
func (h *PushHandler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "push id is required")
		return
	}

	ctx := r.Context()
	if err := h.queries.CancelPushRun(ctx, id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel push: "+err.Error())
		return
	}
	_ = h.queries.CancelPushRunTargets(ctx, id)

	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "id": id})
}

// HandleCancelRunning cancels all pushes stuck in pending/running state.
func (h *PushHandler) HandleCancelRunning(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get IDs of running pushes so we can cancel their targets too.
	ids, err := h.queries.ListRunningPushRunIDs(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list running pushes: "+err.Error())
		return
	}

	for _, id := range ids {
		_ = h.queries.CancelPushRunTargets(ctx, id)
	}

	if err := h.queries.CancelAllRunningPushRuns(ctx); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel running pushes: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "count": len(ids)})
}
