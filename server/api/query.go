// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

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
func (h *QueryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/query", h.HandleQuery)
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
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TraitPath == "" {
		writeJSONError(w, http.StatusBadRequest, "traitPath is required")
		return
	}
	if req.Target == "" {
		writeJSONError(w, http.StatusBadRequest, "target is required")
		return
	}

	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	result, err := h.dispatcher.DispatchQuery(r.Context(), req.TraitPath, req.Target, timeout)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
