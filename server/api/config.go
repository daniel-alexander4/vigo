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

	"git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/publish"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

var _ = time.Now // used by format

// configSyncer is implemented by tungol.Hub to push config to steorras.
// Defined as an interface to avoid an import cycle.
type configSyncer interface {
	SyncConfigToSteorras(ctx context.Context, configDir string) error
}

// auditRecorder is the interface for the tamper-evident audit writer.
// Using an interface lets callers pass nil safely and avoids tight coupling.
type auditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// recordAudit is a nil-safe helper that records an audit event. If w is nil
// (audit not configured), it's a no-op.
func recordAudit(ctx context.Context, w auditRecorder, eventType, actor, source, summary, payload string) {
	if w == nil {
		return
	}
	if err := w.Record(ctx, eventType, actor, source, summary, payload); err != nil {
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
	convergenceMonitor  *publish.ConvergenceMonitor
	publishCfg         config.PublishSection
	aiCfg              config.AISection
	assistant          *ai.Assistant // nil when AI is not enabled
	auditWriter        auditRecorder // nil-safe tamper-evident audit writer
}

// NewConfigHandler creates a ConfigHandler.
func NewConfigHandler(queries *db.Queries, configStore *config.ConfigStore, fleetIndex *fleet.FleetIndex, hub configSyncer) *ConfigHandler {
	return &ConfigHandler{queries: queries, configStore: configStore, fleetIndex: fleetIndex, hub: hub}
}

// SetConvergenceMonitor configures the blast-radius compliance monitor and
// publish settings. When threshold > 0, config reloads start a compliance
// watcher that auto-rolls back if fleet health drops.
func (h *ConfigHandler) SetConvergenceMonitor(monitor *publish.ConvergenceMonitor, cfg config.PublishSection) {
	h.convergenceMonitor = monitor
	h.publishCfg = cfg
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *ConfigHandler) SetAuditWriter(w auditRecorder) {
	h.auditWriter = w
}

// SetAI attaches the AI assistant and config for retraction support.
func (h *ConfigHandler) SetAI(assistant *ai.Assistant, cfg config.AISection) {
	h.assistant = assistant
	h.aiCfg = cfg
}

// RegisterRoutes registers config management endpoints. Mutating
// verbs (publish/rollback/freeze/reload/retract) require
// config.publish; rollback uses its own permission so it can be
// granted independently. Read endpoints (list publishes/versions,
// unused) require fleet.read — config history is fleet metadata, not
// publish-author state. Secret rotation/access callbacks require
// admin (called by the watcher subsystem; CLI/UI never hit these).
func (h *ConfigHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	publish := auth.RequirePermission(auth.PermConfigPublish)
	rollback := auth.RequirePermission(auth.PermConfigRollback)
	admin := auth.RequireAdmin("")

	mux.Handle("GET /api/v1/config/publishes", read(http.HandlerFunc(h.ListPublishes)))
	mux.Handle("POST /api/v1/config/publishes", publish(http.HandlerFunc(h.RecordPublish)))
	mux.Handle("GET /api/v1/config/publishes/{id}", read(http.HandlerFunc(h.GetPublish)))
	mux.Handle("GET /api/v1/config/versions", read(http.HandlerFunc(h.ListVersions)))
	mux.Handle("POST /api/v1/config/rollback/{version}", rollback(http.HandlerFunc(h.Rollback)))
	mux.Handle("POST /api/v1/config/freeze", publish(http.HandlerFunc(h.Freeze)))
	mux.Handle("POST /api/v1/config/unfreeze", publish(http.HandlerFunc(h.Unfreeze)))
	mux.Handle("POST /api/v1/config/reload", publish(http.HandlerFunc(h.ReloadConfig)))
	mux.Handle("POST /api/v1/config/retract", publish(http.HandlerFunc(h.Retract)))
	mux.Handle("GET /api/v1/config/unused", read(http.HandlerFunc(h.HandleUnused)))
	mux.Handle("POST /api/v1/secrets/rotated", admin(http.HandlerFunc(h.SecretRotated)))
	mux.Handle("POST /api/v1/secrets/accessed", admin(http.HandlerFunc(h.SecretAccessed)))
	mux.Handle("POST /api/v1/secrets/event", admin(http.HandlerFunc(h.SecretsLifecycleEvent)))
}

// ReloadConfig re-reads all config files from disk. Called by vigocli
// after config publish syncs files to .live/. In hub mode, also pushes the
// config to all steorras via gRPC (best-effort — steorra failures are logged
// but do not fail the reload).
//
// When a compliance threshold is configured, starts a background monitor
// that watches fleet compliance for the rollback window. If compliance drops
// below the threshold, the previous config snapshot is automatically restored.
func (h *ConfigHandler) ReloadConfig(w http.ResponseWriter, r *http.Request) {
	if err := h.configStore.Reload(); err != nil {
		writeError(w, http.StatusInternalServerError, "config reload failed: "+err.Error())
		return
	}

	// In hub mode, push config to all steorras after successful local reload.
	if h.hub != nil {
		configDir := h.configStore.Dir()
		if err := h.hub.SyncConfigToSteorras(r.Context(), configDir); err != nil {
			slog.Error("config sync to steorras failed", "error", err)
			// Best-effort: don't fail the reload response.
		}
	}

	// Start blast-radius compliance monitor if threshold is configured.
	if h.convergenceMonitor != nil && h.publishCfg.ConvergenceThreshold > 0 {
		window := 5 * time.Minute // default
		if h.publishCfg.RollbackWindow != "" {
			if parsed, err := time.ParseDuration(h.publishCfg.RollbackWindow); err == nil {
				window = parsed
			}
		}
		h.convergenceMonitor.Start(h.publishCfg.ConvergenceThreshold, window)
	}

	// Record tamper-evident audit event.
	recordAudit(r.Context(), h.auditWriter, "config.reload", actorFromRequest(r), "rest", "config reloaded", "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SecretRotated marks a secret as rotated, triggering watch_secret resources
// on affected envoys at their next check-in. Called by vigocli secrets set.
func (h *ConfigHandler) SecretRotated(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	// Reload config so the new secret value is resolved. Surface failure: a
	// swallowed error here would leave cs.entries with stale resolved values
	// and the operator with no signal — same silent class as a /config/reload
	// failure.
	if err := h.configStore.Reload(); err != nil {
		writeError(w, http.StatusInternalServerError, "config reload failed: "+err.Error())
		return
	}

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

// secretsLifecycleEventTypes enumerates the audit event types accepted by the
// /api/v1/secrets/event endpoint. Anything outside this set is rejected so
// the audit chain can't be polluted by arbitrary client-supplied event names.
var secretsLifecycleEventTypes = map[string]string{
	"secrets_unlocked":           "Vault unlocked",
	"secrets_locked":             "Vault locked",
	"secrets_passphrase_rotated": "Unlock passphrase rotated",
	"secrets_passphrase_reset":   "Unlock passphrase reset via break-glass",
}

// SecretsLifecycleEvent records an audit-chain entry for an unlock-gate
// lifecycle action (unlock, lock, rotate, reset) fired by vigocli. The
// payload is restricted to a known event-type vocabulary; client-supplied
// names outside that set are rejected.
func (h *ConfigHandler) SecretsLifecycleEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}
	summary, ok := secretsLifecycleEventTypes[req.Type]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown event type")
		return
	}

	actor := actorFromRequest(r)
	recordAudit(r.Context(), h.auditWriter,
		req.Type, actor, "rest",
		summary,
		fmt.Sprintf(`{"actor":%q}`, actor),
	)

	writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
}

// SecretAccessed records an audit trail entry when a secret is revealed via CLI.
func (h *ConfigHandler) SecretAccessed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	actor := "cli"
	if user := r.Header.Get("X-Vigo-User"); user != "" {
		actor = user
	}

	recordAudit(r.Context(), h.auditWriter,
		"secret_accessed", actor, "rest",
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
		writeError(w, http.StatusInternalServerError, "failed to list publishes")
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
		writeError(w, http.StatusNotFound, "publish not found")
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
		writeError(w, http.StatusInternalServerError, "failed to list versions")
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
		writeError(w, http.StatusBadRequest, "invalid request body")
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
		writeError(w, http.StatusInternalServerError, "failed to record publish")
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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	t, err := time.Parse(time.RFC3339, req.FreezeUntil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid freeze_until timestamp")
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
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}

	if err := publish.RollbackToVersion(publish.HistoryDir, version, publish.LiveDir); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "rolled back", "version": version})
}

// --- Module Retraction ---

// retractRequest is the input to the retract endpoint.
type retractRequest struct {
	Modules []retractModuleInput `json:"modules"`
}

type retractModuleInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// retractResponse is the output from the retract endpoint.
type retractResponse struct {
	Enabled bool                   `json:"enabled"`
	Reason  string                 `json:"reason,omitempty"`
	Results []retractModuleResult  `json:"results,omitempty"`
}

type retractModuleResult struct {
	Name          string                   `json:"name"`
	RetractYAML string                     `json:"retract_yaml"`
	Warnings      []publish.RetractWarning `json:"warnings"`
	Envoys        []retractEnvoyInfo       `json:"envoys"`
}

type retractEnvoyInfo struct {
	Hostname string `json:"hostname"`
	LastRun  string `json:"last_run"`
}

// HandleUnused returns unused modules and match files.
func (h *ConfigHandler) HandleUnused(w http.ResponseWriter, r *http.Request) {
	envoys, err := h.queries.ListActiveEnvoys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "querying envoys: "+err.Error())
		return
	}
	hostnames := make([]string, len(envoys))
	for i, e := range envoys {
		hostnames[i] = e.Hostname
	}
	result := h.configStore.Unused(hostnames)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// Retract generates retract modules for removed modules. Requires both
// publish.retraction.enabled and ai.enabled. The deterministic reversal
// is generated first, then enhanced by AI for exec resources.
func (h *ConfigHandler) Retract(w http.ResponseWriter, r *http.Request) {
	if !h.publishCfg.Retraction.Enabled {
		writeJSON(w, http.StatusOK, retractResponse{
			Enabled: false,
			Reason:  "publish.retraction.enabled is false in server.yaml",
		})
		return
	}
	if !h.aiCfg.Enabled || h.assistant == nil {
		writeJSON(w, http.StatusOK, retractResponse{
			Enabled: false,
			Reason:  "ai.enabled is false in server.yaml (required for retraction)",
		})
		return
	}

	var req retractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Modules) == 0 {
		writeJSON(w, http.StatusOK, retractResponse{Enabled: true})
		return
	}

	var results []retractModuleResult
	for _, mod := range req.Modules {
		result := h.retractModule(r.Context(), mod)
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, retractResponse{
		Enabled: true,
		Results: results,
	})
}

func (h *ConfigHandler) retractModule(ctx context.Context, mod retractModuleInput) retractModuleResult {
	result := retractModuleResult{Name: mod.Name}

	// Step 1: Deterministic reversal.
	draft, warnings, err := publish.GenerateRetractModule(mod.Name, []byte(mod.Content))
	if err != nil {
		result.Warnings = append(result.Warnings, publish.RetractWarning{
			Confidence: "low",
			Message:    "generation failed: " + err.Error(),
		})
		return result
	}
	result.Warnings = warnings

	// Step 2: AI enhancement — improve exec reversals and review the draft.
	enhanced := h.aiEnhanceRetract(ctx, mod.Name, mod.Content, draft)
	if enhanced != "" {
		result.RetractYAML = enhanced
	} else {
		result.RetractYAML = draft
	}

	// Step 3: Look up affected envoys.
	if h.queries != nil {
		rows, err := h.queries.EnvoysWithModule(ctx, mod.Name)
		if err != nil {
			slog.Warn("retract: envoy lookup failed", "module", mod.Name, "error", err)
		} else {
			for _, row := range rows {
				result.Envoys = append(result.Envoys, retractEnvoyInfo{
					Hostname: row.Hostname,
					LastRun:  row.LastRun.(string),
				})
			}
		}
	}

	return result
}

// aiEnhanceRetract sends the deterministic draft to the AI for review and
// improvement of exec resource reversals. Returns the improved YAML or empty
// string if AI enhancement fails (caller falls back to the draft).
func (h *ConfigHandler) aiEnhanceRetract(ctx context.Context, name, original, draft string) string {
	if h.assistant == nil {
		return ""
	}

	prompt := fmt.Sprintf(`You are reviewing an auto-generated retract module for the Vigo configuration management system.

ORIGINAL MODULE (being removed):
%s

DETERMINISTIC RETRACT DRAFT:
%s

Review and improve this retract module:
1. For any exec resources with placeholder commands (TODO), generate the correct reversal command with an appropriate onlyif or unless guard for idempotency.
2. If an exec resource has no meaningful reversal (e.g., "apt-get update", "systemctl daemon-reload"), remove it entirely.
3. Verify the resource ordering makes sense for cleanup (services stop before configs are removed).
4. Keep all non-exec resources as-is — the deterministic reversal is correct for those.

Return ONLY the final YAML content of the retract module. No explanation, no markdown fences, no commentary.`, original, draft)

	resp, err := h.assistant.Ask(ctx, ai.AskRequest{Question: prompt})
	if err != nil {
		slog.Warn("retract: AI enhancement failed", "module", name, "error", err)
		return ""
	}

	// Strip markdown fences if the AI wraps the output despite instructions.
	improved := strings.TrimSpace(resp.Answer)
	improved = strings.TrimPrefix(improved, "```yaml")
	improved = strings.TrimPrefix(improved, "```")
	improved = strings.TrimSuffix(improved, "```")
	improved = strings.TrimSpace(improved)

	if improved == "" {
		return ""
	}

	return improved
}

