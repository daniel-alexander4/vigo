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
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/publish"
)

// writeJSON writes a JSON response with the given status code.
//
// All JSON-response handlers in server/api/ route through this helper —
// it's the single source of truth for "what does a JSON response look
// like" (Content-Type, status, encode, encode-error logging). Sites
// that need extra response headers (Content-Disposition, custom
// Cache-Control, etc.) bypass the helper and emit the direct
// `if err := json.NewEncoder(w).Encode(...); err != nil { … }` shape
// themselves — that's a deliberate long-tail exception.
func writeJSON(w http.ResponseWriter, r *http.Request, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("response encode failed", "path", r.URL.Path, "error", err)
	}
}

var _ = time.Now // used by format

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
// versioning, rollback, and reload. In spanner mode stacks
// distribution is gitback-driven (per ADR-025 Slice 4) — the hub
// no longer fans the stacks out via gRPC.
type ConfigHandler struct {
	queries            *db.Queries
	configStore        *config.ConfigStore
	fleetIndex         *fleet.FleetIndex
	convergenceMonitor *publish.ConvergenceMonitor
	publishCfg         config.PublishSection
	aiCfg              config.AISection
	assistant          *ai.Assistant  // nil when AI is not enabled
	auditWriter        auditRecorder  // nil-safe tamper-evident audit writer
	publishState       *publish.State // freeze deadline owner; required for Freeze/Unfreeze
	metrics            *metrics.Metrics
	liveDir            string                // .live/ path for rollback
	historyDir         string                // history dir for rollback / list-versions
	snapshotCompliance func(context.Context) // optional; fired after a successful reload so dashboard standards-summary refreshes
}

// NewConfigHandler creates a ConfigHandler. publishState carries the
// per-process freeze deadline. liveDir / historyDir are the paths the
// Rollback and ListVersions handlers operate on; empty values fall back
// to the publish.Default* constants.
func NewConfigHandler(queries *db.Queries, configStore *config.ConfigStore, fleetIndex *fleet.FleetIndex, publishState *publish.State, liveDir, historyDir string) *ConfigHandler {
	if liveDir == "" {
		liveDir = publish.DefaultLiveDir
	}
	if historyDir == "" {
		historyDir = publish.DefaultHistoryDir
	}
	return &ConfigHandler{
		queries:      queries,
		configStore:  configStore,
		fleetIndex:   fleetIndex,
		publishState: publishState,
		liveDir:      liveDir,
		historyDir:   historyDir,
	}
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

// SetMetrics attaches the per-instance prometheus collectors so publish
// reloads can be counted for dashboard annotation.
func (h *ConfigHandler) SetMetrics(m *metrics.Metrics) {
	h.metrics = m
}

// SetAI attaches the AI assistant and config for retraction support.
func (h *ConfigHandler) SetAI(assistant *ai.Assistant, cfg config.AISection) {
	h.assistant = assistant
	h.aiCfg = cfg
}

// SetComplianceSnapshotter wires the callback that writes a fresh
// compliance_history row set. Fired in a goroutine after every
// successful ReloadConfig so the dashboard standards-summary — which
// reads from that table — reflects operator changes (waivers added,
// frameworks toggled, scope shifted) within snapshot-runtime seconds
// of the publish. Independent from the daily background snapshotter
// that keeps the sparkline series moving.
func (h *ConfigHandler) SetComplianceSnapshotter(fn func(context.Context)) {
	h.snapshotCompliance = fn
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
	mux.Handle("GET /api/v1/config/version", read(http.HandlerFunc(h.GetVersion)))
	mux.Handle("GET /api/v1/config/status", read(http.HandlerFunc(h.GetStatus)))
	mux.Handle("GET /api/v1/config/versions", read(http.HandlerFunc(h.ListVersions)))
	mux.Handle("POST /api/v1/config/rollback/{version}", rollback(http.HandlerFunc(h.Rollback)))
	mux.Handle("POST /api/v1/config/freeze", publish(http.HandlerFunc(h.Freeze)))
	mux.Handle("POST /api/v1/config/unfreeze", publish(http.HandlerFunc(h.Unfreeze)))
	mux.Handle("POST /api/v1/config/reload", publish(http.HandlerFunc(h.ReloadConfig)))
	mux.Handle("POST /api/v1/config/retract", publish(http.HandlerFunc(h.Retract)))
	mux.Handle("POST /api/v1/secrets/rotated", admin(http.HandlerFunc(h.SecretRotated)))
	mux.Handle("POST /api/v1/secrets/accessed", admin(http.HandlerFunc(h.SecretAccessed)))
	mux.Handle("POST /api/v1/secrets/event", admin(http.HandlerFunc(h.SecretsLifecycleEvent)))
}

// GetVersion returns the current in-memory config-store version,
// which monotonically increments on every successful reload. Used by
// `vigocli config publish` in spanner mode to poll for "did the
// gitback push land in .live/?" — operator sends publish, records
// pre-version, then polls until version increments.
func (h *ConfigHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{
		"version": h.configStore.Version(),
	})
}

// GetStatus returns the config store's reload health: whether the last
// loadAll succeeded, its version, the last error message (empty when
// healthy), and the wall-clock time of the last attempt. Lets operators
// detect a silently-empty config from an IdempotencyErrors at startup
// without tail-ing the server log.
func (h *ConfigHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	var lastErr string
	if e := h.configStore.LastReloadError(); e != nil {
		lastErr = e.Error()
	}
	var lastAt string
	if t := h.configStore.LastReloadAt(); !t.IsZero() {
		lastAt = t.UTC().Format(time.RFC3339)
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"healthy":        lastErr == "",
		"version":        h.configStore.Version(),
		"last_error":     lastErr,
		"last_reload_at": lastAt,
	})
}

// ReloadConfig re-reads all config files from disk. Called by vigocli
// in standalone mode and by the stacks watcher in spanner mode
// (after bare→.live pull on every ref change).
//
// When a compliance threshold is configured, starts a background monitor
// that watches fleet compliance for the rollback window. If compliance drops
// below the threshold, the previous config snapshot is automatically restored.
func (h *ConfigHandler) ReloadConfig(w http.ResponseWriter, r *http.Request) {
	if err := h.configStore.Reload(); err != nil {
		writeError(w, http.StatusInternalServerError, "config reload failed: "+err.Error())
		return
	}
	if h.metrics != nil {
		h.metrics.ConfigPublishesTotal.Inc()
	}
	// Refresh the compliance snapshot so the dashboard summary picks up
	// operator-driven changes (new waivers, framework toggles, scope
	// shifts) without waiting for the daily background tick. Detached
	// from the request context — closing the publish HTTP response
	// shouldn't tear down a snapshot that's already running.
	if h.snapshotCompliance != nil {
		daemon.SafeGo("snapshot-compliance", func() { h.snapshotCompliance(context.Background()) })
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

	writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
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

	recordAudit(r.Context(), h.auditWriter, "secret.rotation_acked", actorFromRequest(r), "rest",
		fmt.Sprintf("secret rotation acked: %s (%d envoys queued)", req.Path, affected), "")

	writeJSON(w, r, http.StatusOK, map[string]any{
		"path":     req.Path,
		"affected": affected,
	})
}

// secretsLifecycleEventTypes enumerates the audit event types accepted by the
// /api/v1/secrets/event endpoint. Anything outside this set is rejected so
// the audit chain can't be polluted by arbitrary client-supplied event names.
var secretsLifecycleEventTypes = map[string]string{
	"secrets.unlocked":           "Vault unlocked",
	"secrets.locked":             "Vault locked",
	"secrets.passphrase_rotated": "Unlock passphrase rotated",
	"secrets.passphrase_reset":   "Unlock passphrase reset via break-glass",
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

	writeJSON(w, r, http.StatusOK, map[string]any{"recorded": true})
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
		"secret.accessed", actor, "rest",
		fmt.Sprintf("Secret revealed: %s", req.Path),
		fmt.Sprintf(`{"path":%q}`, req.Path),
	)

	writeJSON(w, r, http.StatusOK, map[string]any{"recorded": true})
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

	writeJSON(w, r, http.StatusOK, map[string]any{"publishes": result})
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

	writeJSON(w, r, http.StatusOK, map[string]any{
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
	versions, err := publish.ListVersions(h.historyDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list versions")
		return
	}
	if versions == nil {
		versions = []string{}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"versions": versions})
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

	recordAudit(r.Context(), h.auditWriter, "config.publish_recorded", req.Operator, "rest",
		fmt.Sprintf("publish %s recorded: %d changed, %d removed", p.ID, len(req.ChangedFiles), len(req.RemovedFiles)), "")

	writeJSON(w, r, http.StatusCreated, map[string]any{"id": p.ID})
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

	if h.publishState == nil {
		writeError(w, http.StatusInternalServerError, "publish state not configured")
		return
	}
	h.publishState.SetFreezeUntil(t)

	recordAudit(r.Context(), h.auditWriter, "config.freeze", actorFromRequest(r), "rest",
		"publish frozen until "+t.Format(time.RFC3339), "")

	writeJSON(w, r, http.StatusOK, map[string]any{"freeze_until": t.Format(time.RFC3339)})
}

// Unfreeze removes the global config publish freeze.
func (h *ConfigHandler) Unfreeze(w http.ResponseWriter, r *http.Request) {
	if h.publishState == nil {
		writeError(w, http.StatusInternalServerError, "publish state not configured")
		return
	}
	h.publishState.ClearFreeze()

	recordAudit(r.Context(), h.auditWriter, "config.unfreeze", actorFromRequest(r), "rest",
		"publish freeze cleared", "")

	writeJSON(w, r, http.StatusOK, map[string]any{"status": "unfrozen"})
}

// Rollback restores a previous config version.
func (h *ConfigHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	version = strings.TrimSpace(version)
	if version == "" {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}

	if err := publish.RollbackToVersion(h.historyDir, version, h.liveDir); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "config.rollback", actorFromRequest(r), "rest",
		"rolled back to version "+version, "")

	writeJSON(w, r, http.StatusOK, map[string]any{"status": "rolled back", "version": version})
}

// --- Module Retraction ---

// retractRequest is the input to the retract endpoint.
type retractRequest struct {
	Configcrates []retractConfigcrateInput `json:"configcrates"`
}

type retractConfigcrateInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// retractResponse is the output from the retract endpoint.
type retractResponse struct {
	Enabled bool                       `json:"enabled"`
	Reason  string                     `json:"reason,omitempty"`
	Results []retractConfigcrateResult `json:"results,omitempty"`
}

type retractConfigcrateResult struct {
	Name        string                   `json:"name"`
	RetractYAML string                   `json:"retract_yaml"`
	Warnings    []publish.RetractWarning `json:"warnings"`
	Envoys      []retractEnvoyInfo       `json:"envoys"`
}

type retractEnvoyInfo struct {
	Hostname string `json:"hostname"`
	LastRun  string `json:"last_run"`
}

// Retract generates retract modules for removed modules. Requires both
// publish.retraction.enabled and ai.enabled. The deterministic reversal
// is generated first, then enhanced by AI for exec resources.
func (h *ConfigHandler) Retract(w http.ResponseWriter, r *http.Request) {
	if !h.publishCfg.Retraction.Enabled {
		writeJSON(w, r, http.StatusOK, retractResponse{
			Enabled: false,
			Reason:  "publish.retraction.enabled is false in server.yaml",
		})
		return
	}
	if !h.aiCfg.Enabled || h.assistant == nil {
		writeJSON(w, r, http.StatusOK, retractResponse{
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
	if len(req.Configcrates) == 0 {
		writeJSON(w, r, http.StatusOK, retractResponse{Enabled: true})
		return
	}

	var results []retractConfigcrateResult
	moduleNames := make([]string, 0, len(req.Configcrates))
	for _, mod := range req.Configcrates {
		result := h.retractConfigcrate(r.Context(), mod)
		results = append(results, result)
		moduleNames = append(moduleNames, mod.Name)
	}

	recordAudit(r.Context(), h.auditWriter, "config.retract", actorFromRequest(r), "rest",
		fmt.Sprintf("generated retract configcrates for: %s", strings.Join(moduleNames, ", ")), "")

	writeJSON(w, r, http.StatusOK, retractResponse{
		Enabled: true,
		Results: results,
	})
}

func (h *ConfigHandler) retractConfigcrate(ctx context.Context, mod retractConfigcrateInput) retractConfigcrateResult {
	result := retractConfigcrateResult{Name: mod.Name}

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

	prompt := fmt.Sprintf(`You are reviewing an auto-generated retract configcrate for the Vigo configuration management system.

ORIGINAL CONFIGCRATE (being removed):
%s

DETERMINISTIC RETRACT DRAFT:
%s

Review and improve this retract configcrate:
1. For any exec resources with placeholder commands (TODO), generate the correct reversal command with an appropriate onlyif or unless guard for idempotency.
2. If an exec resource has no meaningful reversal (e.g., "apt-get update", "systemctl daemon-reload"), remove it entirely.
3. Verify the resource ordering makes sense for cleanup (services stop before configs are removed).
4. Keep all non-exec resources as-is — the deterministic reversal is correct for those.

Return ONLY the final YAML content of the retract configcrate. No explanation, no markdown fences, no commentary.`, original, draft)

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
