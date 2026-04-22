// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	vigoauth "git.alexander4.io/vigo/server/auth"
)

// TokensHandler provides REST endpoints for API token management.
type TokensHandler struct {
	tokenStore  *vigoauth.TokenStore
	authMethod  string
	auditWriter auditRecorder
}

// SetAuditWriter sets the audit writer for recording token events.
func (h *TokensHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// NewTokensHandler creates a tokens API handler.
func NewTokensHandler(tokenStore *vigoauth.TokenStore, authMethod string) *TokensHandler {
	return &TokensHandler{tokenStore: tokenStore, authMethod: authMethod}
}

// RegisterRoutes mounts token management endpoints.
func (h *TokensHandler) RegisterRoutes(mux *http.ServeMux) {
	authMW := vigoauth.RequireAuth(h.authMethod)
	mux.Handle("POST /api/v1/auth/tokens", authMW(http.HandlerFunc(h.handleCreate)))
	mux.Handle("GET /api/v1/auth/tokens", authMW(http.HandlerFunc(h.handleList)))
	mux.Handle("DELETE /api/v1/auth/tokens/{id}", authMW(http.HandlerFunc(h.handleDelete)))
}

type createTokenRequest struct {
	Name      string `json:"name"`
	ExpiresIn string `json:"expires_in"` // duration like "720h" (30 days)
}

type createTokenResponse struct {
	Token string `json:"token"` // plaintext, shown once
	ID    string `json:"id"`
	Name  string `json:"name"`
}

type tokenListItem struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func (h *TokensHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	u := vigoauth.UserFromContext(r.Context())
	if u == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid expires_in duration")
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	plaintext, token, err := h.tokenStore.CreateToken(r.Context(), u.ID, req.Name, expiresAt)
	if err != nil {
		slog.Error("creating api token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to create token")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "token.create", u.Username, "rest", fmt.Sprintf("created token %s", req.Name), "")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(createTokenResponse{
		Token: plaintext,
		ID:    token.ID,
		Name:  token.Name,
	})
}

func (h *TokensHandler) handleList(w http.ResponseWriter, r *http.Request) {
	u := vigoauth.UserFromContext(r.Context())
	if u == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	rows, err := h.tokenStore.ListByUser(r.Context(), u.ID)
	if err != nil {
		slog.Error("listing api tokens", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	tokens := make([]tokenListItem, len(rows))
	for i, row := range rows {
		tokens[i] = tokenListItem{
			ID:        row.ID,
			UserID:    row.UserID,
			Name:      row.Name,
			CreatedAt: row.CreatedAt,
		}
		if row.LastUsedAt.Valid {
			t := row.LastUsedAt.Time
			tokens[i].LastUsedAt = &t
		}
		if row.ExpiresAt.Valid {
			t := row.ExpiresAt.Time
			tokens[i].ExpiresAt = &t
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tokens)
}

func (h *TokensHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	u := vigoauth.UserFromContext(r.Context())
	if u == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id := r.PathValue("id")
	if err := h.tokenStore.Delete(r.Context(), id); err != nil {
		slog.Error("deleting api token", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to delete token")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "token.revoke", u.Username, "rest", fmt.Sprintf("revoked token %s", id), "")

	w.WriteHeader(http.StatusNoContent)
}
