// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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

// RegisterRoutes mounts the AI endpoint on the given mux. The assistant's
// tools are strictly read-only over fleet data, so the endpoint requires
// fleet.read — the same gate as the web-UI drawer (POST /ui/ask), so the
// two doors to one assistant cannot disagree.
func (h *AIHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	mux.Handle("POST /api/v1/ask", read(http.HandlerFunc(h.handleAsk)))
}

func (h *AIHandler) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req ai.AskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, r, http.StatusOK, resp)
}

func (h *AIHandler) handleAskStream(w http.ResponseWriter, r *http.Request, req ai.AskRequest) {
	sse := ai.NewSSEWriter(w)
	if sse == nil {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
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
		_ = sse.Send(ai.SSEEvent{Type: "error", Text: err.Error()}) // client may have disconnected
	}
}
