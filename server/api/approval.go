// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/publish"
)

// ApprovalHandler manages the two-person change approval workflow for
// config publish. When publish.approval_required is true, direct config
// reload is blocked — changes must go through create → approve.
type ApprovalHandler struct {
	queries           *db.Queries
	configStore       *config.ConfigStore
	publishCfg        config.PublishSection
	hub               configSyncer
	fleetIndex        *fleet.FleetIndex
	convergenceMonitor *publish.ConvergenceMonitor
	auditWriter       auditRecorder
}

// NewApprovalHandler creates an ApprovalHandler.
func NewApprovalHandler(queries *db.Queries, configStore *config.ConfigStore, publishCfg config.PublishSection, hub configSyncer, fleetIndex *fleet.FleetIndex, convergenceMonitor *publish.ConvergenceMonitor) *ApprovalHandler {
	return &ApprovalHandler{
		queries:           queries,
		configStore:       configStore,
		publishCfg:        publishCfg,
		hub:               hub,
		fleetIndex:        fleetIndex,
		convergenceMonitor: convergenceMonitor,
	}
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *ApprovalHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// RegisterRoutes registers the change approval endpoints.
func (h *ApprovalHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/config/changes", h.CreateChange)
	mux.HandleFunc("GET /api/v1/config/changes", h.ListChanges)
	mux.HandleFunc("GET /api/v1/config/changes/{id}", h.GetChange)
	mux.HandleFunc("POST /api/v1/config/changes/{id}/approve", h.ApproveChange)
	mux.HandleFunc("POST /api/v1/config/changes/{id}/reject", h.RejectChange)
}

// CreateChange creates a pending config change request. Any existing pending
// changes are cancelled first (only one pending change at a time).
func (h *ApprovalHandler) CreateChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DiffSummary string `json:"diff_summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	requestedBy := usernameFromRequest(r)

	// Cancel any existing pending changes.
	if err := h.queries.CancelPendingConfigChanges(r.Context()); err != nil {
		slog.Error("failed to cancel existing pending changes", "error", err)
	}

	// Compute expiry time from approval_timeout (default 24h).
	timeout := 24 * time.Hour
	if h.publishCfg.ApprovalTimeout != "" {
		if parsed, err := time.ParseDuration(h.publishCfg.ApprovalTimeout); err == nil {
			timeout = parsed
		}
	}
	expiresAt := time.Now().UTC().Add(timeout)

	change, err := h.queries.CreateConfigChange(r.Context(), db.CreateConfigChangeParams{
		RequestedBy: requestedBy,
		ExpiresAt:   expiresAt,
		DiffSummary: req.DiffSummary,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create change request")
		return
	}

	writeJSON(w, http.StatusCreated, changeToJSON(change))
}

// ApproveChange approves a pending change and triggers config reload.
// The approver must be a different user than the requester (two-person rule).
func (h *ApprovalHandler) ApproveChange(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid change id")
		return
	}

	change, err := h.queries.GetConfigChange(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "change not found")
		return
	}

	if change.Status != "pending" {
		writeJSONError(w, http.StatusConflict, "change is not pending (status: "+change.Status+")")
		return
	}

	approvedBy := usernameFromRequest(r)

	// Two-person rule: approver must differ from requester.
	if approvedBy == change.RequestedBy {
		writeJSONError(w, http.StatusForbidden, "approver must be a different user than the requester")
		return
	}

	if err := h.queries.ApproveConfigChange(r.Context(), db.ApproveConfigChangeParams{
		ApprovedBy: sql.NullString{String: approvedBy, Valid: true},
		ID:         id,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to approve change")
		return
	}

	// Perform the config reload.
	if err := h.configStore.Reload(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "config reload failed after approval: "+err.Error())
		return
	}

	// In hub mode, push config to spokes.
	if h.hub != nil {
		configDir := h.configStore.Dir()
		if err := h.hub.SyncConfigToSpokes(r.Context(), configDir); err != nil {
			slog.Error("config sync to spokes failed after approval", "error", err)
		}
	}

	// Start blast-radius compliance monitor if threshold is configured.
	if h.convergenceMonitor != nil && h.publishCfg.ConvergenceThreshold > 0 {
		window := 5 * time.Minute
		if h.publishCfg.RollbackWindow != "" {
			if parsed, pErr := time.ParseDuration(h.publishCfg.RollbackWindow); pErr == nil {
				window = parsed
			}
		}
		h.convergenceMonitor.Start(h.publishCfg.ConvergenceThreshold, window)
	}

	slog.Info("config change approved and applied",
		"change_id", id,
		"requested_by", change.RequestedBy,
		"approved_by", approvedBy,
	)

	// Record tamper-evident audit event.
	recordAudit(r.Context(), h.auditWriter, "config.approve", approvedBy,
		fmt.Sprintf("approved change #%d from %s", id, change.RequestedBy), "")

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "approved",
		"change_id":    id,
		"requested_by": change.RequestedBy,
		"approved_by":  approvedBy,
	})
}

// RejectChange rejects a pending change with an optional reason.
func (h *ApprovalHandler) RejectChange(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid change id")
		return
	}

	change, err := h.queries.GetConfigChange(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "change not found")
		return
	}

	if change.Status != "pending" {
		writeJSONError(w, http.StatusConflict, "change is not pending (status: "+change.Status+")")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // optional body

	approvedBy := usernameFromRequest(r)

	if err := h.queries.RejectConfigChange(r.Context(), db.RejectConfigChangeParams{
		ApprovedBy:   sql.NullString{String: approvedBy, Valid: true},
		RejectReason: sql.NullString{String: req.Reason, Valid: req.Reason != ""},
		ID:           id,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to reject change")
		return
	}

	slog.Info("config change rejected",
		"change_id", id,
		"requested_by", change.RequestedBy,
		"rejected_by", approvedBy,
		"reason", req.Reason,
	)

	// Record tamper-evident audit event.
	reason := req.Reason
	if reason == "" {
		reason = "no reason given"
	}
	recordAudit(r.Context(), h.auditWriter, "config.reject", approvedBy,
		fmt.Sprintf("rejected change #%d: %s", id, reason), "")

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "rejected",
		"change_id": id,
	})
}

// ListChanges returns all pending config changes.
func (h *ApprovalHandler) ListChanges(w http.ResponseWriter, r *http.Request) {
	changes, err := h.queries.ListPendingConfigChanges(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list changes")
		return
	}

	result := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		result = append(result, changeToJSON(c))
	}

	writeJSON(w, http.StatusOK, map[string]any{"changes": result})
}

// GetChange returns a single config change by ID.
func (h *ApprovalHandler) GetChange(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid change id")
		return
	}

	change, err := h.queries.GetConfigChange(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "change not found")
		return
	}

	writeJSON(w, http.StatusOK, changeToJSON(change))
}

// StartExpiryLoop runs a background goroutine that expires pending config
// changes that have passed their expiry time. Runs every minute.
func StartExpiryLoop(ctx context.Context, queries *db.Queries) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := queries.ExpirePendingConfigChanges(ctx); err != nil {
				slog.Error("failed to expire pending config changes", "error", err)
			}
		}
	}
}

// usernameFromRequest extracts the username from the auth context.
// Returns "anonymous" if no user is present (auth disabled).
func usernameFromRequest(r *http.Request) string {
	u := vigoauth.UserFromContext(r.Context())
	if u != nil && u.Username != "" {
		return u.Username
	}
	return "anonymous"
}

// changeToJSON converts a ConfigChange to a JSON-friendly map.
func changeToJSON(c db.ConfigChange) map[string]any {
	m := map[string]any{
		"id":           c.ID,
		"status":       c.Status,
		"requested_by": c.RequestedBy,
		"requested_at": c.RequestedAt.Format(time.RFC3339),
		"expires_at":   c.ExpiresAt.Format(time.RFC3339),
		"diff_summary": c.DiffSummary,
	}
	if c.ApprovedBy.Valid {
		m["approved_by"] = c.ApprovedBy.String
	}
	if c.ResolvedAt.Valid {
		m["resolved_at"] = c.ResolvedAt.Time.Format(time.RFC3339)
	}
	if c.RejectReason.Valid {
		m["reject_reason"] = c.RejectReason.String
	}
	return m
}
