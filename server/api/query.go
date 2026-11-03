// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	isgrpc "git.alexander4.io/vigo/server/grpc"
)

// QueryDispatcher dispatches live queries to connected agents.
type QueryDispatcher interface {
	DispatchQuery(ctx context.Context, traitPath, targetSpec string, timeout time.Duration) (*isgrpc.LiveQueryResult, error)
}

// QueryHandler serves the REST endpoint for live trait queries.
type QueryHandler struct {
	dispatcher QueryDispatcher
}

// NewQueryHandler creates a QueryHandler.
func NewQueryHandler(dispatcher QueryDispatcher) *QueryHandler {
	return &QueryHandler{dispatcher: dispatcher}
}

// RegisterRoutes registers the query endpoint on the given mux.
// Live queries dispatch to all connected agents and surface trait
// values, so the route requires query.execute (which only admins
// hold by default).
func (h *QueryHandler) RegisterRoutes(mux *http.ServeMux) {
	exec := auth.RequirePermission(auth.PermQueryExecute)
	mux.Handle("POST /api/v1/query", exec(http.HandlerFunc(h.HandleQuery)))
}

type queryRequest struct {
	TraitPath string `json:"traitPath"`
	Target    string `json:"target"`
	Timeout   int    `json:"timeout"`
}

// HandleQuery dispatches a live query and returns results.
func (h *QueryHandler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TraitPath == "" {
		writeError(w, http.StatusBadRequest, "traitPath is required")
		return
	}
	if req.Target == "" {
		writeError(w, http.StatusBadRequest, "target is required")
		return
	}

	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	result, err := h.dispatcher.DispatchQuery(r.Context(), req.TraitPath, req.Target, timeout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, r, http.StatusOK, result)
}
