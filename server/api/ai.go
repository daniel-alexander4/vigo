// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/auth"
)

// AIHandler serves the /api/v1/ask endpoint.
type AIHandler struct {
	assistant *ai.Assistant
}

// NewAIHandler creates an AI handler.
func NewAIHandler(assistant *ai.Assistant) *AIHandler {
	return &AIHandler{assistant: assistant}
}

// RegisterRoutes mounts the AI endpoint on the given mux. The
// assistant has tool access to live fleet data and can dispatch
// queries, so the endpoint requires query.execute (admin-only by
// default).
func (h *AIHandler) RegisterRoutes(mux *http.ServeMux) {
	exec := auth.RequirePermission(auth.PermQueryExecute)
	mux.Handle("POST /api/v1/ask", exec(http.HandlerFunc(h.handleAsk)))
}

func (h *AIHandler) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req ai.AskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Question == "" {
		writeJSONError(w, http.StatusBadRequest, "question is required")
		return
	}

	// Check Accept header for SSE streaming.
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		h.handleAskStream(w, r, req)
		return
	}

	resp, err := h.assistant.Ask(r.Context(), req)
	if err != nil {
		slog.Error("ai ask failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *AIHandler) handleAskStream(w http.ResponseWriter, r *http.Request, req ai.AskRequest) {
	sse := ai.NewSSEWriter(w)
	if sse == nil {
		writeJSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	err := h.assistant.AskStream(r.Context(), req, func(event ai.SSEEvent) {
		if err := sse.Send(event); err != nil {
			slog.Error("ai sse write error", "error", err)
		}
	})
	if err != nil {
		slog.Error("ai stream failed", "error", err)
		// If we already started writing SSE, we can't send a JSON error.
		// Send an error event instead.
		sse.Send(ai.SSEEvent{Type: "error", Text: err.Error()})
	}
}
