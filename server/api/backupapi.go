// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/backup"
)

// BackupHandler provides REST endpoints for backup operations.
type BackupHandler struct {
	serverYAMLPath string
	serverRoot     string
	dbDriver       string
	dbDSN          string
	version        string
	authMethod     string
}

// NewBackupHandler creates a backup API handler.
func NewBackupHandler(serverYAMLPath, serverRoot, dbDriver, dbDSN, version, authMethod string) *BackupHandler {
	return &BackupHandler{
		serverYAMLPath: serverYAMLPath,
		serverRoot:     serverRoot,
		dbDriver:       dbDriver,
		dbDSN:          dbDSN,
		version:        version,
		authMethod:     authMethod,
	}
}

// RegisterRoutes mounts backup endpoints.
func (h *BackupHandler) RegisterRoutes(mux *http.ServeMux) {
	adminMW := auth.RequireAdmin(h.authMethod)
	mux.Handle("POST /api/v1/backup/snapshot", adminMW(http.HandlerFunc(h.handleSnapshot)))
	mux.Handle("GET /api/v1/backup/download", adminMW(http.HandlerFunc(h.handleDownload)))
}

func (h *BackupHandler) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	tmpFile, err := os.CreateTemp("", "vigo-snapshot-*.tar.gz")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := backup.CreateArchive(tmpPath, backup.CreateOptions{
		ServerYAMLPath: h.serverYAMLPath,
		ServerRoot:     h.serverRoot,
		DBDriver:       h.dbDriver,
		DBDSN:          h.dbDSN,
		VigoVersion:  h.version,
	}); err != nil {
		slog.Error("backup snapshot failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "backup failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","message":"snapshot created"}`)
}

func (h *BackupHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	tmpFile, err := os.CreateTemp("", "vigo-download-*.tar.gz")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := backup.CreateArchive(tmpPath, backup.CreateOptions{
		ServerYAMLPath: h.serverYAMLPath,
		ServerRoot:     h.serverRoot,
		DBDriver:       h.dbDriver,
		DBDSN:          h.dbDSN,
		VigoVersion:  h.version,
	}); err != nil {
		slog.Error("backup download failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "backup failed: "+err.Error())
		return
	}

	filename := fmt.Sprintf("vigo-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(w, r, tmpPath)
}
