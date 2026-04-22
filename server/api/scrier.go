// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// ScrierHandler provides REST endpoints for Scrier user management.
type ScrierHandler struct {
	queries     *db.Queries
	authMethod  string
	auditWriter auditRecorder
}

// SetAuditWriter sets the audit writer.
func (h *ScrierHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// NewScrierHandler creates a Scrier API handler.
func NewScrierHandler(queries *db.Queries, authMethod string) *ScrierHandler {
	return &ScrierHandler{queries: queries, authMethod: authMethod}
}

// RegisterRoutes mounts Scrier user management endpoints.
func (h *ScrierHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	mux.Handle("GET /api/v1/scrier/users", adminMW(http.HandlerFunc(h.handleList)))
	mux.Handle("PUT /api/v1/scrier/users/{username}/osuser", adminMW(http.HandlerFunc(h.handleSetOSUser)))
}

func (h *ScrierHandler) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := h.queries.ListScrierUsers(r.Context())
	if err != nil {
		slog.Error("listing scrier users", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	type scrierUser struct {
		Username string `json:"username"`
		OSUser   string `json:"os_user"`
	}
	users := make([]scrierUser, 0, len(rows))
	for _, r := range rows {
		users = append(users, scrierUser{
			Username: r.Username,
			OSUser:   r.ScrierUsername,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"users": users})
}

type setOSUserRequest struct {
	OSUser string `json:"os_user"`
}

func (h *ScrierHandler) handleSetOSUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")

	var req setOSUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	osUser := strings.TrimSpace(req.OSUser)
	if osUser == "" {
		writeJSONError(w, http.StatusBadRequest, "os_user is required")
		return
	}

	if err := h.queries.SetScrierUsername(r.Context(), db.SetScrierUsernameParams{
		ScrierUsername: osUser,
		Username:       username,
	}); err != nil {
		slog.Error("setting scrier OS user", "user", username, "os_user", osUser, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to set OS user")
		return
	}

	actor := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		actor = u.Username
	}
	recordAudit(r.Context(), h.auditWriter, "scrier.osuser_set", actor, "rest",
		fmt.Sprintf("mapped Scrier user %s → OS user %s", username, osUser), "")

	w.WriteHeader(http.StatusNoContent)
}
