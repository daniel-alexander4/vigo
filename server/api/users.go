// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/crypto/ssh"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/secrets"
)

// validateSSHPubkey parses an OpenSSH-formatted public key (one line: type +
// base64 + optional comment). Returns the trimmed key on success or a
// non-nil error message suitable for surfacing to API callers. Empty input
// is accepted (treated as "no pubkey set"); validation only runs on
// non-empty values.
func validateSSHPubkey(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmed)); err != nil {
		return "", fmt.Errorf("invalid ssh_public_key: %w", err)
	}
	return trimmed, nil
}

// UsersHandler provides REST endpoints for user management.
type UsersHandler struct {
	store       *auth.UserStore
	authMethod  string
	auditWriter auditRecorder
	secrets     secrets.Provider
}

// SetAuditWriter sets the audit writer for recording user management events.
func (h *UsersHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// SetSecretsProvider sets the secrets provider for TOTP secret storage.
func (h *UsersHandler) SetSecretsProvider(p secrets.Provider) { h.secrets = p }

// NewUsersHandler creates a users API handler.
func NewUsersHandler(store *auth.UserStore, authMethod string) *UsersHandler {
	return &UsersHandler{store: store, authMethod: authMethod}
}

// RegisterRoutes mounts user management endpoints.
func (h *UsersHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	authMW := auth.RequireAuth(h.authMethod)
	mux.Handle("GET /api/v1/users", adminMW(http.HandlerFunc(h.handleList)))
	mux.Handle("POST /api/v1/users", http.HandlerFunc(h.handleCreate)) // special: first-user bootstrap
	mux.Handle("PUT /api/v1/users/{id}", adminMW(http.HandlerFunc(h.handleUpdate)))
	mux.Handle("DELETE /api/v1/users/{id}", adminMW(http.HandlerFunc(h.handleDelete)))
	mux.Handle("POST /api/v1/users/totp/setup", authMW(http.HandlerFunc(h.handleTOTPSetup)))
	mux.Handle("POST /api/v1/users/totp/confirm", authMW(http.HandlerFunc(h.handleTOTPConfirm)))
	mux.Handle("POST /api/v1/users/totp/disable", authMW(http.HandlerFunc(h.handleTOTPDisable)))
}

func (h *UsersHandler) handleList(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("listing users", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Role == "" {
		req.Role = "viewer"
	}
	if !auth.IsValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("role must be one of %v", auth.ValidRoles))
		return
	}
	pubkey, err := validateSSHPubkey(req.SSHPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Require admin role for user creation.
	u := auth.UserFromContext(r.Context())
	if u == nil || !u.IsAdmin() {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}

	user, err := h.store.Create(r.Context(), db.InsertUserParams{
		Username:     req.Username,
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		Role:         req.Role,
		SshPublicKey: pubkey,
	})
	if err != nil {
		slog.Error("creating user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "user.create", u.Username, "rest", fmt.Sprintf("created user %s with role %s", req.Username, req.Role), "")

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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role != "" && !auth.IsValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("role must be one of %v", auth.ValidRoles))
		return
	}
	pubkey, err := validateSSHPubkey(req.SSHPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.SSHPublicKey = pubkey

	// Fetch existing user to preserve unchanged fields.
	existing, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("fetching user", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	role := existing.Role
	if req.Role != "" {
		role = req.Role
	}

	// Recalculate permissions when role changes; preserve existing otherwise.
	var perms string
	if req.Role != "" && req.Role != existing.Role {
		perms = auth.PermissionsJSON(role)
	} else {
		// Keep existing permissions as JSON.
		data, _ := json.Marshal(existing.Permissions)
		perms = string(data)
	}

	if err := h.store.Update(r.Context(), db.UpdateUserParams{
		ID:           id,
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		Role:         role,
		Permissions:  perms,
		SshPublicKey: req.SSHPublicKey,
	}); err != nil {
		slog.Error("updating user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update user")
		return
	}

	// Audit role changes only.
	if req.Role != "" && req.Role != existing.Role {
		actor := ""
		if u := auth.UserFromContext(r.Context()); u != nil {
			actor = u.Username
		}
		recordAudit(r.Context(), h.auditWriter, "user.role_change", actor, "rest", fmt.Sprintf("%s role changed from %s to %s", existing.Username, existing.Role, req.Role), "")
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *UsersHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Cannot delete yourself.
	u := auth.UserFromContext(r.Context())
	if u != nil && u.ID == id {
		writeError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}

	// Look up username before deleting for the audit log.
	deleted, _ := h.store.GetByID(r.Context(), id)

	if err := h.store.Delete(r.Context(), id); err != nil {
		slog.Error("deleting user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	actor := ""
	if u != nil {
		actor = u.Username
	}
	deletedName := id
	if deleted != nil {
		deletedName = deleted.Username
	}
	recordAudit(r.Context(), h.auditWriter, "user.delete", actor, "rest", fmt.Sprintf("deleted user %s", deletedName), "")

	w.WriteHeader(http.StatusNoContent)
}

// handleTOTPSetup generates a TOTP secret and returns the otpauth URL.
// The secret is stored in the secrets provider but TOTP is not yet enabled —
// the user must confirm with a valid code via /totp/confirm.
func (h *UsersHandler) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.secrets == nil {
		writeError(w, http.StatusInternalServerError, "secrets provider not configured")
		return
	}

	secret, url, err := auth.GenerateTOTPSecret(u.Username)
	if err != nil {
		slog.Error("generating totp secret", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to generate TOTP secret")
		return
	}

	// Store the secret (not yet enabled — must confirm first).
	secretKey := fmt.Sprintf("vigo/web/auth/totp/%s", u.Username)
	if err := h.secrets.(interface{ Set(string, string) error }).Set(secretKey, secret); err != nil {
		slog.Error("storing totp secret", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store TOTP secret")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"secret": secret,
		"url":    url,
	})
}

// handleTOTPConfirm verifies a TOTP code and enables TOTP for the user.
// This is the second step after /totp/setup — the user must prove they
// configured their authenticator app correctly.
func (h *UsersHandler) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Get the pending secret.
	secretKey := fmt.Sprintf("vigo/web/auth/totp/%s", u.Username)
	secret, err := h.secrets.Get(r.Context(), secretKey)
	if err != nil || secret == "" {
		writeError(w, http.StatusBadRequest, "no TOTP setup pending — call /totp/setup first")
		return
	}

	// Validate the code.
	if !auth.ValidateTOTPCode(req.Code, secret) {
		writeError(w, http.StatusBadRequest, "invalid code — check your authenticator app")
		return
	}

	// Generate recovery codes.
	plainCodes, hashedJSON, err := auth.GenerateRecoveryCodes()
	if err != nil {
		slog.Error("generating recovery codes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to generate recovery codes")
		return
	}

	// Enable TOTP in DB.
	if err := h.store.EnableTOTP(r.Context(), u.ID, hashedJSON); err != nil {
		slog.Error("enabling totp", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to enable TOTP")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "user.totp_enable", u.Username, "rest", fmt.Sprintf("TOTP enabled for %s", u.Username), "")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":         "enabled",
		"recovery_codes": plainCodes,
	})
}

// handleTOTPDisable disables TOTP for the authenticated user.
func (h *UsersHandler) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := h.store.DisableTOTP(r.Context(), u.ID); err != nil {
		slog.Error("disabling totp", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to disable TOTP")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "user.totp_disable", u.Username, "rest", fmt.Sprintf("TOTP disabled for %s", u.Username), "")

	w.WriteHeader(http.StatusNoContent)
}
