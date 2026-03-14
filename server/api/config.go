// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/publish"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

var _ = time.Now // used by format

// ConfigHandler provides REST endpoints for config publish history,
// versioning, and rollback.
type ConfigHandler struct {
	queries *db.Queries
}

// NewConfigHandler creates a ConfigHandler.
func NewConfigHandler(queries *db.Queries) *ConfigHandler {
	return &ConfigHandler{queries: queries}
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
		_ = json.Unmarshal([]byte(p.ChangedFiles), &changed)
		_ = json.Unmarshal([]byte(p.RemovedFiles), &removed)
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
	_ = json.Unmarshal([]byte(p.ChangedFiles), &changed)
	_ = json.Unmarshal([]byte(p.RemovedFiles), &removed)

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
