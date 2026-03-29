// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/db"
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
	mux.Handle("PUT /api/v1/scrier/users/{username}/pubkey", adminMW(http.HandlerFunc(h.handleSetPubkey)))
	mux.Handle("DELETE /api/v1/scrier/users/{username}/pubkey", adminMW(http.HandlerFunc(h.handleRemovePubkey)))
}

func (h *ScrierHandler) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := h.queries.ListUsersWithSSHPubkey(r.Context())
	if err != nil {
		slog.Error("listing scrier users", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	type scrierUser struct {
		Username  string `json:"username"`
		Pubkey    string `json:"pubkey"`
	}
	users := make([]scrierUser, 0, len(rows))
	for _, r := range rows {
		users = append(users, scrierUser{
			Username: r.Username,
			Pubkey:   r.SshPublicKey,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"users": users})
}

type setPubkeyRequest struct {
	Pubkey string `json:"pubkey"`
}

func (h *ScrierHandler) handleSetPubkey(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")

	var req setPubkeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	pubkey := strings.TrimSpace(req.Pubkey)
	if pubkey == "" {
		writeJSONError(w, http.StatusBadRequest, "pubkey is required")
		return
	}

	// Validate it looks like an SSH public key.
	if !strings.HasPrefix(pubkey, "ssh-") && !strings.HasPrefix(pubkey, "ecdsa-") {
		writeJSONError(w, http.StatusBadRequest, "invalid SSH public key format")
		return
	}

	if err := h.queries.SetUserSSHPubkey(r.Context(), db.SetUserSSHPubkeyParams{
		SshPublicKey: pubkey,
		Username:     username,
	}); err != nil {
		slog.Error("setting scrier pubkey", "user", username, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to set pubkey")
		return
	}

	actor := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		actor = u.Username
	}
	recordAudit(r.Context(), h.auditWriter, "scrier.pubkey_set", actor, "rest",
		fmt.Sprintf("set SSH pubkey for Scrier user %s", username), "")

	w.WriteHeader(http.StatusNoContent)
}

func (h *ScrierHandler) handleRemovePubkey(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")

	if err := h.queries.SetUserSSHPubkey(r.Context(), db.SetUserSSHPubkeyParams{
		SshPublicKey: "",
		Username:     username,
	}); err != nil {
		slog.Error("removing scrier pubkey", "user", username, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to remove pubkey")
		return
	}

	actor := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		actor = u.Username
	}
	recordAudit(r.Context(), h.auditWriter, "scrier.pubkey_remove", actor, "rest",
		fmt.Sprintf("removed SSH pubkey for Scrier user %s", username), "")

	w.WriteHeader(http.StatusNoContent)
}
