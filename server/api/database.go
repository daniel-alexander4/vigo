// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// DatabaseHandler serves /api/v1/db/* endpoints for database operations.
type DatabaseHandler struct {
	sqlDB   *sql.DB
	queries *db.Queries
	driver  string // "sqlite" or "postgres"
	dsn     string // for file size (SQLite only)
}

// NewDatabaseHandler creates a database operations handler.
func NewDatabaseHandler(sqlDB *sql.DB, queries *db.Queries, driver, dsn string) *DatabaseHandler {
	return &DatabaseHandler{sqlDB: sqlDB, queries: queries, driver: driver, dsn: dsn}
}

// RegisterRoutes mounts the database endpoints. All DB-admin verbs
// require admin role — vacuum/checkpoint mutate WAL state, /db/query
// runs arbitrary SQL. Read-only stats are gated on admin too because
// the table-row counts surface internal record counts (envoys,
// secrets, audit chain entries) that aren't on any public surface.
func (h *DatabaseHandler) RegisterRoutes(mux *http.ServeMux) {
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/db/stats", admin(http.HandlerFunc(h.handleStats)))
	mux.Handle("GET /api/v1/db/tables", admin(http.HandlerFunc(h.handleTables)))
	mux.Handle("GET /api/v1/db/integrity", admin(http.HandlerFunc(h.handleIntegrity)))
	mux.Handle("POST /api/v1/db/vacuum", admin(http.HandlerFunc(h.handleVacuum)))
	mux.Handle("POST /api/v1/db/checkpoint", admin(http.HandlerFunc(h.handleCheckpoint)))
	mux.Handle("POST /api/v1/db/query", admin(http.HandlerFunc(h.handleQuery)))
}

// handleStats returns database size, WAL size, page count, and table row counts.
func (h *DatabaseHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := map[string]any{
		"driver": h.driver,
	}

	if h.driver == "sqlite" {
		// File sizes.
		if info, err := os.Stat(h.dsn); err == nil {
			stats["file_size"] = info.Size()
		}
		walPath := h.dsn + "-wal"
		if info, err := os.Stat(walPath); err == nil {
			stats["wal_size"] = info.Size()
		}

		// Page count and size.
		var pageCount, pageSize int64
		h.sqlDB.QueryRow("PRAGMA page_count").Scan(&pageCount)
		h.sqlDB.QueryRow("PRAGMA page_size").Scan(&pageSize)
		stats["page_count"] = pageCount
		stats["page_size"] = pageSize
	}

	// Table row counts.
	tables, _ := h.tableRowCounts()
	stats["tables"] = tables

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleTables returns all tables with row counts.
func (h *DatabaseHandler) handleTables(w http.ResponseWriter, r *http.Request) {
	tables, err := h.tableRowCounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tables)
}

// handleIntegrity runs PRAGMA integrity_check (SQLite) or checks connection health.
func (h *DatabaseHandler) handleIntegrity(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{"driver": h.driver}

	if h.driver == "sqlite" {
		var check string
		err := h.sqlDB.QueryRow("PRAGMA integrity_check").Scan(&check)
		if err != nil {
			result["status"] = "error"
			result["error"] = err.Error()
		} else {
			result["status"] = check
			result["ok"] = check == "ok"
		}

		// Also check foreign keys.
		rows, err := h.sqlDB.Query("PRAGMA foreign_key_check")
		if err == nil {
			var violations []string
			for rows.Next() {
				var table, rowid, parent, fkidx string
				rows.Scan(&table, &rowid, &parent, &fkidx)
				violations = append(violations, fmt.Sprintf("%s row %s → %s", table, rowid, parent))
			}
			rows.Close()
			result["foreign_key_violations"] = len(violations)
			if len(violations) > 0 {
				result["foreign_key_details"] = violations
			}
		}
	} else {
		err := h.sqlDB.Ping()
		if err != nil {
			result["status"] = "error"
			result["error"] = err.Error()
			result["ok"] = false
		} else {
			result["status"] = "ok"
			result["ok"] = true
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleVacuum runs VACUUM to reclaim space. SQLite only.
func (h *DatabaseHandler) handleVacuum(w http.ResponseWriter, r *http.Request) {
	if h.driver != "sqlite" {
		writeError(w, http.StatusBadRequest, "VACUUM is only available for SQLite")
		return
	}

	slog.Info("database VACUUM requested")

	// Get size before.
	var sizeBefore int64
	if info, err := os.Stat(h.dsn); err == nil {
		sizeBefore = info.Size()
	}

	if _, err := h.sqlDB.Exec("VACUUM"); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("VACUUM failed: %v", err))
		return
	}

	// Get size after.
	var sizeAfter int64
	if info, err := os.Stat(h.dsn); err == nil {
		sizeAfter = info.Size()
	}

	slog.Info("database VACUUM complete", "before", sizeBefore, "after", sizeAfter, "reclaimed", sizeBefore-sizeAfter)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"before":    sizeBefore,
		"after":     sizeAfter,
		"reclaimed": sizeBefore - sizeAfter,
	})
}

// handleCheckpoint forces a WAL checkpoint. SQLite only.
func (h *DatabaseHandler) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	if h.driver != "sqlite" {
		writeError(w, http.StatusBadRequest, "checkpoint is only available for SQLite")
		return
	}

	var pagesWritten, pagesTotal int
	err := h.sqlDB.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(new(int), &pagesTotal, &pagesWritten)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("checkpoint failed: %v", err))
		return
	}

	slog.Info("WAL checkpoint complete", "pages_total", pagesTotal, "pages_written", pagesWritten)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"pages_total":   pagesTotal,
		"pages_written": pagesWritten,
	})
}

// handleQuery executes a read-only SQL query. Rejects writes.
func (h *DatabaseHandler) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SQL == "" {
		writeError(w, http.StatusBadRequest, "sql is required")
		return
	}

	// Block write operations.
	upper := strings.ToUpper(strings.TrimSpace(req.SQL))
	for _, prefix := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE", "ATTACH", "DETACH", "REINDEX", "VACUUM", "PRAGMA"} {
		if strings.HasPrefix(upper, prefix) {
			writeError(w, http.StatusForbidden, "write operations are not allowed — use read-only SELECT queries")
			return
		}
	}

	rows, err := h.sqlDB.Query(req.SQL)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("query failed: %v", err))
		return
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var results []map[string]any

	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("scan failed: %v", err))
			return
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			v := values[i]
			if b, ok := v.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = v
			}
		}
		results = append(results, row)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"columns": cols,
		"rows":    results,
		"count":   len(results),
	})
}

// tableRowCounts returns row counts for all tables.
func (h *DatabaseHandler) tableRowCounts() (map[string]int64, error) {
	var tableQuery string
	if h.driver == "sqlite" {
		tableQuery = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
	} else {
		tableQuery = "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename"
	}

	rows, err := h.sqlDB.Query(tableQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]int64)
	for rows.Next() {
		var name string
		rows.Scan(&name)
		// Validate table name is alphanumeric+underscores only (defense in depth —
		// names come from system catalog but we quote defensively).
		safe := true
		for _, c := range name {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				safe = false
				break
			}
		}
		if !safe {
			continue
		}
		var count int64
		h.sqlDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", name)).Scan(&count)
		tables[name] = count
	}
	return tables, nil
}
