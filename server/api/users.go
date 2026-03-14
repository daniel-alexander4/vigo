// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/hobnob/hobnob/server/auth"
	"github.com/hobnob/hobnob/server/db"
)

// UsersHandler provides REST endpoints for user management.
type UsersHandler struct {
	store      *auth.UserStore
	authMethod string
}

// NewUsersHandler creates a users API handler.
func NewUsersHandler(store *auth.UserStore, authMethod string) *UsersHandler {
	return &UsersHandler{store: store, authMethod: authMethod}
}

// RegisterRoutes mounts user management endpoints.
func (h *UsersHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	mux.Handle("GET /api/v1/users", adminMW(http.HandlerFunc(h.handleList)))
	mux.Handle("POST /api/v1/users", http.HandlerFunc(h.handleCreate)) // special: first-user bootstrap
	mux.Handle("PUT /api/v1/users/{id}", adminMW(http.HandlerFunc(h.handleUpdate)))
	mux.Handle("DELETE /api/v1/users/{id}", adminMW(http.HandlerFunc(h.handleDelete)))
}

func (h *UsersHandler) handleList(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("listing users", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

type createUserRequest struct {
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	SSHPublicKey string `json:"ssh_public_key"`
}

func (h *UsersHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" {
		writeJSONError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Role == "" {
		req.Role = "viewer"
	}
	if req.Role != "admin" && req.Role != "viewer" {
		writeJSONError(w, http.StatusBadRequest, "role must be admin or viewer")
		return
	}

	// First-user bootstrap: allow creation without auth when no users exist.
	if h.authMethod != "none" {
		count, err := h.store.CountUsers(r.Context())
		if err != nil {
			slog.Error("counting users", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if count > 0 {
			// Require admin role for subsequent creates.
			u := auth.UserFromContext(r.Context())
			if u == nil || !u.IsAdmin() {
				writeJSONError(w, http.StatusForbidden, "admin role required")
				return
			}
		} else {
			// First user is always admin.
			req.Role = "admin"
		}
	}

	user, err := h.store.Create(r.Context(), db.InsertUserParams{
		Username:     req.Username,
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		Role:         req.Role,
		SshPublicKey: req.SSHPublicKey,
	})
	if err != nil {
		slog.Error("creating user", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

type updateUserRequest struct {
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	SSHPublicKey string `json:"ssh_public_key"`
}

func (h *UsersHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role != "" && req.Role != "admin" && req.Role != "viewer" {
		writeJSONError(w, http.StatusBadRequest, "role must be admin or viewer")
		return
	}

	// Fetch existing user to preserve unchanged fields.
	existing, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("fetching user", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		writeJSONError(w, http.StatusNotFound, "user not found")
		return
	}

	role := existing.Role
	if req.Role != "" {
		role = req.Role
	}

	if err := h.store.Update(r.Context(), db.UpdateUserParams{
		ID:           id,
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		Role:         role,
		SshPublicKey: req.SSHPublicKey,
	}); err != nil {
		slog.Error("updating user", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to update user")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *UsersHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Cannot delete yourself.
	u := auth.UserFromContext(r.Context())
	if u != nil && u.ID == id {
		writeJSONError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		slog.Error("deleting user", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
