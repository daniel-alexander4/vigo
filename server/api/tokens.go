// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	hobnobauth "github.com/hobnob/hobnob/server/auth"
)

// TokensHandler provides REST endpoints for API token management.
type TokensHandler struct {
	tokenStore *hobnobauth.TokenStore
	authMethod string
}

// NewTokensHandler creates a tokens API handler.
func NewTokensHandler(tokenStore *hobnobauth.TokenStore, authMethod string) *TokensHandler {
	return &TokensHandler{tokenStore: tokenStore, authMethod: authMethod}
}

// RegisterRoutes mounts token management endpoints.
func (h *TokensHandler) RegisterRoutes(mux *http.ServeMux) {
	authMW := hobnobauth.RequireAuth(h.authMethod)
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

func (h *TokensHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	u := hobnobauth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			http.Error(w, "invalid expires_in duration", http.StatusBadRequest)
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	plaintext, token, err := h.tokenStore.CreateToken(r.Context(), u.ID, req.Name, expiresAt)
	if err != nil {
		slog.Error("creating api token", "error", err)
		http.Error(w, "failed to create token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(createTokenResponse{
		Token: plaintext,
		ID:    token.ID,
		Name:  token.Name,
	})
}

func (h *TokensHandler) handleList(w http.ResponseWriter, r *http.Request) {
	u := hobnobauth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tokens, err := h.tokenStore.ListByUser(r.Context(), u.ID)
	if err != nil {
		slog.Error("listing api tokens", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tokens)
}

func (h *TokensHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	u := hobnobauth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if err := h.tokenStore.Delete(r.Context(), id); err != nil {
		slog.Error("deleting api token", "error", err)
		http.Error(w, "failed to delete token", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
