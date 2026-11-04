// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
	store          *auth.UserStore
	auditWriter    auditRecorder
	secrets        secrets.Provider
	sessions       *auth.SessionManager // optional; required for session revocation on role change/delete
	queries        *db.Queries          // optional; required for admin-username validation
	serverHostname string               // optional; configured server.hostname (local agent envoy)
}

// SetAuditWriter sets the audit writer for recording user management events.
func (h *UsersHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// SetSecretsProvider sets the secrets provider for TOTP secret storage.
func (h *UsersHandler) SetSecretsProvider(p secrets.Provider) { h.secrets = p }

// SetSessions wires in the SCS session manager so role changes and user
// deletes can destroy the affected user's cached sessions immediately.
// Without it the demoted user retains their old role until the next idle
// timeout. Tests that don't exercise session invalidation can leave it
// unset — the destroy calls are nil-guarded.
func (h *UsersHandler) SetSessions(sm *auth.SessionManager) { h.sessions = sm }

// SetAdminValidation wires in the DB queries and the server's own hostname
// so admin user creation can validate the requested username against the
// local agent's `users.human` trait. Without this, admin-role creates are
// rejected with a clear "validation not configured" error — call it from
// production wiring in main.go after constructing the handler.
func (h *UsersHandler) SetAdminValidation(q *db.Queries, serverHostname string) {
	h.queries = q
	h.serverHostname = serverHostname
}

// NewUsersHandler creates a users API handler.
func NewUsersHandler(store *auth.UserStore) *UsersHandler {
	return &UsersHandler{store: store}
}

// validateAdminUsername confirms that `username` is a human OS account on the
// server host, by consulting the local agent's `users.human` trait. The local
// agent is identified by `server.hostname` from server.yaml. Returns nil on
// success; otherwise an error suitable for surfacing verbatim to the API
// caller (it names the remediation step).
func (h *UsersHandler) validateAdminUsername(ctx context.Context, username string) error {
	if h.queries == nil || h.serverHostname == "" {
		return fmt.Errorf("admin user validation is not configured on this server (missing server.hostname or DB wiring); see install docs")
	}
	envoy, err := h.queries.GetEnvoyByHostname(ctx, h.serverHostname)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("local agent (hostname %q) not yet enrolled — run `curl -sSfk https://localhost:8443/bootstrap | sudo sh` from the server host, then retry", h.serverHostname)
	} else if err != nil {
		return fmt.Errorf("looking up local agent envoy: %w", err)
	}
	traitsRow, err := h.queries.GetLatestEnvoyTraits(ctx, envoy.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("local agent is enrolled but has not yet reported traits; wait one check-in cycle (~30s) and retry")
	} else if err != nil {
		return fmt.Errorf("fetching local agent traits: %w", err)
	}

	// Traits column is a JSON object keyed by collector name. The `users`
	// collector reports {human, os, thirdparty} — `human` is the uid≥1000,
	// real-shell list, the only category eligible for admin mapping.
	var allTraits map[string]json.RawMessage
	if err := json.Unmarshal([]byte(traitsRow.Traits), &allTraits); err != nil {
		return fmt.Errorf("parsing local agent traits: %w", err)
	}
	usersRaw, ok := allTraits["users"]
	if !ok {
		return fmt.Errorf("local agent has not collected the `users` trait yet; wait one check-in cycle and retry")
	}
	var usersTrait struct {
		Human []string `json:"human"`
	}
	if err := json.Unmarshal(usersRaw, &usersTrait); err != nil {
		return fmt.Errorf("parsing local agent users trait: %w", err)
	}
	for _, name := range usersTrait.Human {
		if name == username {
			return nil
		}
	}
	return fmt.Errorf("admin username %q is not a human OS user on the server host (got: %v); create the OS user (uid≥1000, real shell) first, then retry", username, usersTrait.Human)
}

// RegisterRoutes mounts user management endpoints.
func (h *UsersHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin()
	authMW := auth.RequireAuth()
	mux.Handle("GET /api/v1/users", adminMW(http.HandlerFunc(h.handleList)))
	mux.Handle("POST /api/v1/users", adminMW(http.HandlerFunc(h.handleCreate)))
	mux.Handle("PUT /api/v1/users/{id}", adminMW(http.HandlerFunc(h.handleUpdate)))
	mux.Handle("DELETE /api/v1/users/{id}", adminMW(http.HandlerFunc(h.handleDelete)))
	mux.Handle("POST /api/v1/users/{id}/revoke-sessions", adminMW(http.HandlerFunc(h.handleRevokeSessions)))
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
	writeJSON(w, r, http.StatusOK, users)
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

	u := auth.UserFromContext(r.Context()) // admin, enforced by adminMW

	// Admin webusers must map 1:1 with a human OS user on the server host.
	// Viewer / compliance users have no such constraint — they're one-offs.
	if req.Role == "admin" {
		if err := h.validateAdminUsername(r.Context(), req.Username); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// An admin webuser must carry an SSH pubkey — it backs browser-scrier
		// landing and audit attribution. The CLI auto-seeds it; reject here
		// so no client can land a pubkey-less admin.
		if pubkey == "" {
			writeError(w, http.StatusBadRequest, "admin role requires an SSH public key — create with `vigocli webusers create --username "+req.Username+" --role admin` on the server host so the pubkey is auto-seeded from the OS user's ~/.ssh/id_ed25519.pub")
			return
		}
	}

	user, err := h.store.Create(r.Context(), db.InsertUserParams{
		Username:     req.Username,
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		Role:         req.Role,
		SshPublicKey: pubkey,
	})
	if err != nil {
		if errors.Is(err, auth.ErrReservedUsername) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("username %q is reserved", req.Username))
			return
		}
		if errors.Is(err, auth.ErrUserExists) {
			writeError(w, http.StatusConflict, fmt.Sprintf("user %q already exists — use `vigocli webusers set-password --username %s` to change its password", req.Username, req.Username))
			return
		}
		slog.Error("creating user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "user.create", u.Username, "rest", fmt.Sprintf("created user %s with role %s", req.Username, req.Role), "")

	writeJSON(w, r, http.StatusCreated, user)
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

	// Same OS-user mapping invariant on promotion to admin.
	if req.Role == "admin" && req.Role != existing.Role {
		if err := h.validateAdminUsername(r.Context(), existing.Username); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
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

	// Each updatable field is a partial update: an omitted (empty) field
	// preserves the stored value rather than blanking it. This lets a
	// role-only PUT (`webusers set-role`) leave display name, email, and
	// the SSH key intact — the latter matters because wiping an admin's
	// pubkey would break isowebauth/scrier landing on the next login.
	displayName := existing.DisplayName
	if req.DisplayName != "" {
		displayName = req.DisplayName
	}
	email := existing.Email
	if req.Email != "" {
		email = req.Email
	}
	sshKey := existing.SSHPublicKey
	if req.SSHPublicKey != "" {
		sshKey = req.SSHPublicKey
	}

	// An admin webuser must carry an SSH pubkey — it backs browser-scrier
	// landing and audit attribution. Reject a promotion (or any update)
	// that would leave an admin without one, at the source rather than
	// surfacing later as a failed scrier session.
	if role == "admin" && sshKey == "" {
		writeError(w, http.StatusBadRequest, "admin role requires an SSH public key — run `vigocli webusers set-role --username "+existing.Username+" --role admin` on the server host so the pubkey is auto-seeded from the OS user's ~/.ssh/id_ed25519.pub")
		return
	}

	if err := h.store.Update(r.Context(), db.UpdateUserParams{
		ID:           id,
		DisplayName:  displayName,
		Email:        email,
		Role:         role,
		Permissions:  perms,
		SshPublicKey: sshKey,
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

		// Destroy the updated user's active sessions so the new role takes
		// effect on their next request, not after the 15-minute idle timeout.
		// Best-effort: a destroy-loop error doesn't fail the request — the
		// role change already committed.
		if h.sessions != nil {
			n, err := h.sessions.DestroyUserSessions(r.Context(), id)
			if err != nil {
				slog.Warn("destroying sessions after role change", "user", existing.Username, "destroyed", n, "error", err)
			} else if n > 0 {
				slog.Info("destroyed sessions after role change", "user", existing.Username, "count", n)
			}
		}
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

	// Look up the user first — for the audit username and to return a clean
	// 404 (rather than an opaque 500/false-success) when the id is unknown.
	deleted, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("fetching user before delete", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if deleted == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("user %q not found", id))
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		slog.Error("deleting user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	// Destroy the deleted user's active sessions so any in-flight browser
	// sessions stop authenticating immediately rather than continuing to
	// resolve to a now-orphaned User snapshot until idle timeout.
	if h.sessions != nil {
		deletedName := id
		if deleted != nil {
			deletedName = deleted.Username
		}
		n, err := h.sessions.DestroyUserSessions(r.Context(), id)
		if err != nil {
			slog.Warn("destroying sessions after user delete", "user", deletedName, "destroyed", n, "error", err)
		} else if n > 0 {
			slog.Info("destroyed sessions after user delete", "user", deletedName, "count", n)
		}
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

// handleRevokeSessions destroys every active session for the target user.
// It's the server-side hook for credential changes that happen off the
// server — notably `vigocli webusers set-password`, which writes the new
// hash straight into the secrets vault and never otherwise touches the
// server, so without this an attacker's pre-reset session would keep
// authenticating until idle timeout. Admin-gated; the caller's own session
// is unaffected unless they revoke themselves.
func (h *UsersHandler) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	target, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("fetching user before session revoke", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("user %q not found", id))
		return
	}

	if h.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "session management is not configured on this server")
		return
	}

	n, err := h.sessions.DestroyUserSessions(r.Context(), id)
	if err != nil {
		slog.Warn("destroying sessions on revoke", "user", target.Username, "destroyed", n, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke all sessions")
		return
	}

	actor := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		actor = u.Username
	}
	recordAudit(r.Context(), h.auditWriter, "user.sessions_revoked", actor, "rest", fmt.Sprintf("revoked %d session(s) for %s", n, target.Username), "")

	writeJSON(w, r, http.StatusOK, map[string]any{"revoked": n})
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

	recordAudit(r.Context(), h.auditWriter, "user.totp_setup", u.Username, "rest",
		fmt.Sprintf("TOTP setup initiated for %s (awaiting confirm)", u.Username), "")

	writeJSON(w, r, http.StatusOK, map[string]string{
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
	ok, enrollStep := auth.ValidateTOTPCodeStep(req.Code, secret, time.Now())
	if !ok {
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

	// Enable TOTP in DB, seeding the consumed step so the enrollment code can't
	// be replayed at login.
	if err := h.store.EnableTOTP(r.Context(), u.ID, hashedJSON, enrollStep); err != nil {
		slog.Error("enabling totp", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to enable TOTP")
		return
	}

	recordAudit(r.Context(), h.auditWriter, "user.totp_enable", u.Username, "rest", fmt.Sprintf("TOTP enabled for %s", u.Username), "")

	writeJSON(w, r, http.StatusOK, map[string]any{
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
