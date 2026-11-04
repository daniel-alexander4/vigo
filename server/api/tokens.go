// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	vigoauth "git.alexander4.io/vigo/server/auth"
)

// TokensHandler provides REST endpoints for API token management.
type TokensHandler struct {
	tokenStore  *vigoauth.TokenStore
	userStore   *vigoauth.UserStore
	auditWriter auditRecorder
}

// SetAuditWriter sets the audit writer for recording token events.
func (h *TokensHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// NewTokensHandler creates a tokens API handler. userStore is required to
// validate that every minted token is owned by a real web user.
func NewTokensHandler(tokenStore *vigoauth.TokenStore, userStore *vigoauth.UserStore) *TokensHandler {
	return &TokensHandler{tokenStore: tokenStore, userStore: userStore}
}

// RegisterRoutes mounts token management endpoints.
func (h *TokensHandler) RegisterRoutes(mux *http.ServeMux) {
	authMW := vigoauth.RequireAuth()
	mux.Handle("POST /api/v1/auth/tokens", authMW(http.HandlerFunc(h.handleCreate)))
	mux.Handle("GET /api/v1/auth/tokens", authMW(http.HandlerFunc(h.handleList)))
	mux.Handle("DELETE /api/v1/auth/tokens/{id}", authMW(http.HandlerFunc(h.handleDelete)))
}

type createTokenRequest struct {
	Name      string `json:"name"`
	ExpiresIn string `json:"expires_in"` // duration like "720h" (30 days)
	User      string `json:"user"`       // owner username; only the local-admin CLI may set it
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
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_in duration")
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	// Resolve the token owner. By default a caller owns the tokens it mints.
	// The local-admin principal (root on the server host) has no users-table
	// row, so it MUST name a real owner via --user — otherwise the token is
	// orphaned and can never authenticate (the owner is resolved at validation
	// time). Only that principal may mint for another user: this endpoint is
	// auth-gated, not admin-gated, so letting a real web user set --user would
	// let a viewer mint an admin-owned token.
	ownerID := u.ID
	if req.User != "" {
		if u.ID != vigoauth.LocalAdminUserID {
			writeError(w, http.StatusForbidden, "only the server-host admin CLI may set --user")
			return
		}
		target, err := h.userStore.GetByUsername(r.Context(), req.User)
		if err != nil {
			slog.Error("resolving token owner", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if target == nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("no such web user: %s", req.User))
			return
		}
		ownerID = target.ID
	}
	// Every token must be owned by a real web user. Catches the local-admin
	// principal minting with no --user.
	if owner, err := h.userStore.GetByID(r.Context(), ownerID); err != nil {
		slog.Error("validating token owner", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	} else if owner == nil {
		writeError(w, http.StatusBadRequest, "token owner must be a real web user — pass --user <username> when creating from the server host")
		return
	}

	plaintext, token, err := h.tokenStore.CreateToken(r.Context(), ownerID, req.Name, expiresAt)
	if err != nil {
		slog.Error("creating api token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create token")
		return
	}

	auditDetail := fmt.Sprintf("created token %s", req.Name)
	if ownerID != u.ID {
		auditDetail = fmt.Sprintf("created token %s for user %s", req.Name, req.User)
	}
	recordAudit(r.Context(), h.auditWriter, "token.create", u.Username, "rest", auditDetail, "")

	writeJSON(w, r, http.StatusCreated, createTokenResponse{
		Token: plaintext,
		ID:    token.ID,
		Name:  token.Name,
	})
}

func (h *TokensHandler) handleList(w http.ResponseWriter, r *http.Request) {
	u := vigoauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	rows, err := h.tokenStore.ListByUser(r.Context(), u.ID)
	if err != nil {
		slog.Error("listing api tokens", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
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

	writeJSON(w, r, http.StatusOK, tokens)
}

func (h *TokensHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	u := vigoauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id := r.PathValue("id")

	// Authorize: a caller may delete its own token, or any token if it is an
	// admin (offboarding a departed user). Without this, the auth-only (not
	// admin-only) route lets any authenticated user — incl. a viewer — revoke
	// another user's token, including an admin's, by guessing its ID.
	tok, err := h.tokenStore.GetByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	if err != nil {
		slog.Error("looking up api token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tok.UserID != u.ID && !u.IsAdmin() {
		recordAudit(r.Context(), h.auditWriter, "token.revoke_denied", u.Username, "rest", fmt.Sprintf("denied revoke of token %s owned by another user", id), "")
		writeError(w, http.StatusForbidden, "not your token")
		return
	}

	if err := h.tokenStore.Delete(r.Context(), id); err != nil {
		slog.Error("deleting api token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete token")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "token.revoke", u.Username, "rest", fmt.Sprintf("revoked token %s", id), "")

	w.WriteHeader(http.StatusNoContent)
}
