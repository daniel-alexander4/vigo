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
	"git.alexander4.io/vigo/server/curators3"
)

// S3CredentialsHandler manages SigV4 credentials for the curator S3 API
// (ADR-027). Admin-only — these are fleet credentials. The store is nil under
// any non-`local` secrets backend (the SigV4 secret derivation needs the
// master key); every route then returns 503.
type S3CredentialsHandler struct {
	store       *curators3.CredStore
	auditWriter auditRecorder
}

// NewS3CredentialsHandler creates the handler. `store` is nil under a
// non-`local` secrets backend; the server sets it via SetStore once the
// LocalProvider is in hand.
func NewS3CredentialsHandler(store *curators3.CredStore) *S3CredentialsHandler {
	return &S3CredentialsHandler{store: store}
}

// SetStore wires the credential store. Called once at startup under the
// `local` backend; left nil otherwise (the routes then 503).
func (h *S3CredentialsHandler) SetStore(store *curators3.CredStore) { h.store = store }

// SetAuditWriter wires the tamper-evident audit chain.
func (h *S3CredentialsHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// RegisterRoutes mounts the credential-management endpoints (on :8443).
func (h *S3CredentialsHandler) RegisterRoutes(mux *http.ServeMux) {
	admin := vigoauth.RequireAdmin("")
	mux.Handle("POST /api/v1/swarm/curator/s3-credentials", admin(http.HandlerFunc(h.handleCreate)))
	mux.Handle("GET /api/v1/swarm/curator/s3-credentials", admin(http.HandlerFunc(h.handleList)))
	mux.Handle("DELETE /api/v1/swarm/curator/s3-credentials/{access_key_id}", admin(http.HandlerFunc(h.handleDelete)))
}

func (h *S3CredentialsHandler) requireStore(w http.ResponseWriter) bool {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable,
			"the curator S3 API requires the `local` secrets backend")
		return false
	}
	return true
}

type createS3CredentialRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

type createS3CredentialResponse struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"` // shown once — derived, never stored
	Scope           string `json:"scope"`
	Name            string `json:"name"`
}

type s3CredentialListItem struct {
	AccessKeyID string    `json:"access_key_id"`
	Scope       string    `json:"scope"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
}

func (h *S3CredentialsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	var req createS3CredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := h.store.Create(r.Context(), req.Name, req.Scope)
	if err != nil {
		slog.Error("creating curator s3 credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create credential")
		return
	}
	recordAudit(r.Context(), h.auditWriter, "curator.s3_credential.create", actorFromRequest(r), "rest",
		fmt.Sprintf("minted curator S3 credential %s (scope %s)", created.AccessKeyID, created.Scope), "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createS3CredentialResponse{
		AccessKeyID:     created.AccessKeyID,
		SecretAccessKey: created.SecretAccessKey,
		Scope:           created.Scope,
		Name:            created.Name,
	})
}

func (h *S3CredentialsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	rows, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("listing curator s3 credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	items := make([]s3CredentialListItem, len(rows))
	for i, row := range rows {
		items[i] = s3CredentialListItem{
			AccessKeyID: row.AccessKeyID,
			Scope:       row.Scope,
			Name:        row.Name,
			CreatedAt:   row.CreatedAt,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *S3CredentialsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("access_key_id")
	if err := h.store.Revoke(r.Context(), id); err != nil {
		slog.Error("revoking curator s3 credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke credential")
		return
	}
	recordAudit(r.Context(), h.auditWriter, "curator.s3_credential.revoke", actorFromRequest(r), "rest",
		fmt.Sprintf("revoked curator S3 credential %s", id), "")
	w.WriteHeader(http.StatusNoContent)
}
