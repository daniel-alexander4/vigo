// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/publish"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

var _ = time.Now // used by format

// configSyncer is implemented by spanner.Hub to push config to spokes.
// Defined as an interface to avoid an import cycle.
type configSyncer interface {
	SyncConfigToSpokes(ctx context.Context, configDir string) error
}

// auditRecorder is the interface for the tamper-evident audit writer.
// Using an interface lets callers pass nil safely and avoids tight coupling.
type auditRecorder interface {
	Record(ctx context.Context, eventType, actor, summary, payload string) error
}

// recordAudit is a nil-safe helper that records an audit event. If w is nil
// (audit not configured), it's a no-op.
func recordAudit(ctx context.Context, w auditRecorder, eventType, actor, summary, payload string) {
	if w == nil {
		return
	}
	if err := w.Record(ctx, eventType, actor, summary, payload); err != nil {
		slog.Error("audit record failed", "type", eventType, "error", err)
	}
}

// ConfigHandler provides REST endpoints for config publish history,
// versioning, rollback, and reload.
type ConfigHandler struct {
	queries            *db.Queries
	configStore        *config.ConfigStore
	fleetIndex         *fleet.FleetIndex
	hub                configSyncer // nil unless hub mode
	complianceMonitor  *publish.ComplianceMonitor
	publishCfg         config.PublishSection
	auditWriter        auditRecorder // nil-safe tamper-evident audit writer
}

// NewConfigHandler creates a ConfigHandler.
func NewConfigHandler(queries *db.Queries, configStore *config.ConfigStore, fleetIndex *fleet.FleetIndex, hub configSyncer) *ConfigHandler {
	return &ConfigHandler{queries: queries, configStore: configStore, fleetIndex: fleetIndex, hub: hub}
}

// SetComplianceMonitor configures the blast-radius compliance monitor and
// publish settings. When threshold > 0, config reloads start a compliance
// watcher that auto-rolls back if fleet health drops.
func (h *ConfigHandler) SetComplianceMonitor(monitor *publish.ComplianceMonitor, cfg config.PublishSection) {
	h.complianceMonitor = monitor
	h.publishCfg = cfg
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *ConfigHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// RegisterRoutes registers config management endpoints.
func (h *ConfigHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config/publishes", h.ListPublishes)
	mux.HandleFunc("POST /api/v1/config/publishes", h.RecordPublish)
	mux.HandleFunc("GET /api/v1/config/publishes/{id}", h.GetPublish)
	mux.HandleFunc("GET /api/v1/config/versions", h.ListVersions)
	mux.HandleFunc("POST /api/v1/config/rollback/{version}", h.Rollback)
	mux.HandleFunc("POST /api/v1/config/freeze", h.Freeze)
	mux.HandleFunc("POST /api/v1/config/unfreeze", h.Unfreeze)
	mux.HandleFunc("POST /api/v1/config/reload", h.ReloadConfig)
	mux.HandleFunc("POST /api/v1/secrets/rotated", h.SecretRotated)
	mux.HandleFunc("POST /api/v1/secrets/accessed", h.SecretAccessed)
}

// ReloadConfig re-reads all config files from disk. Called by vigogo
// after config publish syncs files to .live/. In hub mode, also pushes the
// config to all spokes via gRPC (best-effort — spoke failures are logged
// but do not fail the reload).
//
// When a compliance threshold is configured, starts a background monitor
// that watches fleet compliance for the rollback window. If compliance drops
// below the threshold, the previous config snapshot is automatically restored.
func (h *ConfigHandler) ReloadConfig(w http.ResponseWriter, r *http.Request) {
	// When approval is required, block direct reload — callers must use
	// the change approval flow (POST /api/v1/config/changes → approve).
	if h.publishCfg.ApprovalRequired {
		writeJSONError(w, http.StatusForbidden, "approval required: submit a change via POST /api/v1/config/changes and have a second user approve it")
		return
	}

	if err := h.configStore.Reload(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "config reload failed: "+err.Error())
		return
	}

	// In hub mode, push config to all spokes after successful local reload.
	if h.hub != nil {
		configDir := h.configStore.Dir()
		if err := h.hub.SyncConfigToSpokes(r.Context(), configDir); err != nil {
			slog.Error("config sync to spokes failed", "error", err)
			// Best-effort: don't fail the reload response.
		}
	}

	// Start blast-radius compliance monitor if threshold is configured.
	if h.complianceMonitor != nil && h.publishCfg.ComplianceThreshold > 0 {
		window := 5 * time.Minute // default
		if h.publishCfg.RollbackWindow != "" {
			if parsed, err := time.ParseDuration(h.publishCfg.RollbackWindow); err == nil {
				window = parsed
			}
		}
		h.complianceMonitor.Start(h.publishCfg.ComplianceThreshold, window)
	}

	// Record tamper-evident audit event.
	recordAudit(r.Context(), h.auditWriter, "config.reload", usernameFromRequest(r), "config reloaded", "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SecretRotated marks a secret as rotated, triggering watch_secret resources
// on affected envoys at their next check-in. Called by vigogo secrets set.
func (h *ConfigHandler) SecretRotated(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeJSONError(w, http.StatusBadRequest, "path is required")
		return
	}

	// Reload config so the new secret value is resolved.
	_ = h.configStore.Reload()

	// Find envoys referencing this secret via their match patterns.
	patterns := h.configStore.EnvoysReferencingSecret(req.Path)
	affected := 0

	for _, pattern := range patterns {
		// Resolve pattern to actual envoy IDs via FleetIndex (handles globs).
		if h.fleetIndex != nil {
			targets := h.fleetIndex.ListByGlob(pattern)
			for _, t := range targets {
				_ = h.queries.AppendRotatedSecret(r.Context(), t.EnvoyID, req.Path)
				h.fleetIndex.AppendPendingRotatedSecret(t.EnvoyID, req.Path)
				affected++
			}
		} else {
			// Fallback: exact hostname lookup.
			envoy, err := h.queries.GetEnvoyByHostname(r.Context(), pattern)
			if err != nil || envoy.ID == "" {
				continue
			}
			_ = h.queries.AppendRotatedSecret(r.Context(), envoy.ID, req.Path)
			affected++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":     req.Path,
		"affected": affected,
	})
}

// SecretAccessed records an audit trail entry when a secret is revealed via CLI.
func (h *ConfigHandler) SecretAccessed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeJSONError(w, http.StatusBadRequest, "path is required")
		return
	}

	actor := "cli"
	if user := r.Header.Get("X-Vigo-User"); user != "" {
		actor = user
	}

	recordAudit(r.Context(), h.auditWriter,
		"secret_accessed", actor,
		fmt.Sprintf("Secret revealed: %s", req.Path),
		fmt.Sprintf(`{"path":%q}`, req.Path),
	)

	writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
}

// ListPublishes returns recent config publishes.
func (h *ConfigHandler) ListPublishes(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	publishes, err := h.queries.ListConfigPublishes(r.Context(), int64(limit))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list publishes")
		return
	}

	type publishEntry struct {
		ID           string   `json:"id"`
		Operator     string   `json:"operator"`
		PublishedAt  string   `json:"published_at"`
		ChangedFiles []string `json:"changed_files"`
		RemovedFiles []string `json:"removed_files"`
	}

	result := make([]publishEntry, 0, len(publishes))
	for _, p := range publishes {
		var changed, removed []string
		if err := json.Unmarshal([]byte(p.ChangedFiles), &changed); err != nil {
			slog.Warn("malformed changed_files JSON", "publish_id", p.ID, "error", err)
		}
		if err := json.Unmarshal([]byte(p.RemovedFiles), &removed); err != nil {
			slog.Warn("malformed removed_files JSON", "publish_id", p.ID, "error", err)
		}
		result = append(result, publishEntry{
			ID:           p.ID,
			Operator:     p.Operator,
			PublishedAt:  p.PublishedAt.Format("2006-01-02T15:04:05Z"),
			ChangedFiles: changed,
			RemovedFiles: removed,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"publishes": result})
}

// GetPublish returns details for a single config publish.
func (h *ConfigHandler) GetPublish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := h.queries.GetConfigPublish(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "publish not found")
		return
	}

	var changed, removed []string
	if err := json.Unmarshal([]byte(p.ChangedFiles), &changed); err != nil {
		slog.Warn("malformed changed_files JSON", "publish_id", p.ID, "error", err)
	}
	if err := json.Unmarshal([]byte(p.RemovedFiles), &removed); err != nil {
		slog.Warn("malformed removed_files JSON", "publish_id", p.ID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"publish": map[string]any{
			"id":            p.ID,
			"operator":      p.Operator,
			"published_at":  p.PublishedAt.Format("2006-01-02T15:04:05Z"),
			"changed_files": changed,
			"removed_files": removed,
			"diff_summary":  p.DiffSummary,
		},
	})
}

// ListVersions returns available config snapshot versions for rollback.
func (h *ConfigHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := publish.ListVersions(publish.HistoryDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list versions")
		return
	}
	if versions == nil {
		versions = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

// RecordPublish records a config publish for audit trail.
func (h *ConfigHandler) RecordPublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operator     string   `json:"operator"`
		ChangedFiles []string `json:"changed_files"`
		RemovedFiles []string `json:"removed_files"`
		DiffSummary  string   `json:"diff_summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	changedJSON, _ := json.Marshal(req.ChangedFiles)
	removedJSON, _ := json.Marshal(req.RemovedFiles)

	p, err := h.queries.InsertConfigPublish(r.Context(), db.InsertConfigPublishParams{
		Operator:     req.Operator,
		ChangedFiles: string(changedJSON),
		RemovedFiles: string(removedJSON),
		DiffSummary:  req.DiffSummary,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to record publish")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"id": p.ID})
}

// Freeze sets a global config publish freeze.
func (h *ConfigHandler) Freeze(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FreezeUntil string `json:"freeze_until"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	t, err := time.Parse(time.RFC3339, req.FreezeUntil)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid freeze_until timestamp")
		return
	}

	publish.FreezeUntil = &t
	writeJSON(w, http.StatusOK, map[string]any{"freeze_until": t.Format(time.RFC3339)})
}

// Unfreeze removes the global config publish freeze.
func (h *ConfigHandler) Unfreeze(w http.ResponseWriter, r *http.Request) {
	publish.FreezeUntil = nil
	writeJSON(w, http.StatusOK, map[string]any{"status": "unfrozen"})
}

// Rollback restores a previous config version.
func (h *ConfigHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	version = strings.TrimSpace(version)
	if version == "" {
		writeJSONError(w, http.StatusBadRequest, "version is required")
		return
	}

	if err := publish.RollbackToVersion(publish.HistoryDir, version, publish.LiveDir); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "rolled back", "version": version})
}
