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
	"sync"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// DatabaseHandler serves /api/v1/db/* endpoints for database operations.
type DatabaseHandler struct {
	sqlDB       *sql.DB
	queries     *db.Queries
	driver      string // "sqlite" or "postgres"
	dsn         string // for file size (SQLite only)
	auditWriter auditRecorder

	// roDB is a dedicated read-only handle used exclusively by /db/query.
	// On SQLite it is opened with query_only/immutable-equivalent flags so
	// the connection itself rejects any write — the keyword denylist in
	// handleQuery is only defense-in-depth (it's bypassable via CTEs and
	// multi-statement SQL). Lazily opened on first query; nil falls back to
	// sqlDB. On Postgres a true RO connection needs server-side role/txn
	// setup, so it stays nil and the denylist remains the only guard there
	// (the supported release is SQLite-only).
	roDB    *sql.DB
	roOnce  sync.Once
	roDBErr error
}

// NewDatabaseHandler creates a database operations handler.
func NewDatabaseHandler(sqlDB *sql.DB, queries *db.Queries, driver, dsn string) *DatabaseHandler {
	return &DatabaseHandler{sqlDB: sqlDB, queries: queries, driver: driver, dsn: dsn}
}

// readOnlyDB returns a SQLite connection on which writes are rejected at the
// connection level (mattn's _query_only pragma + immutable busy/timeout
// pragmas). Opened once, lazily. Returns (nil, nil) for non-SQLite drivers so
// the caller falls back to the shared handle + keyword denylist.
func (h *DatabaseHandler) readOnlyDB() (*sql.DB, error) {
	if h.driver != "sqlite" {
		return nil, nil
	}
	h.roOnce.Do(func() {
		// _query_only=on makes the connection refuse writes even via
		// ATTACH/CTE/trigger paths — this is the authoritative guard. The
		// file: URI form is required for mode= params; a bare path or an
		// in-memory ":memory:" name gets the prefix, an already-file: DSN
		// (e.g. a shared-cache test DB) keeps its own.
		base := h.dsn
		if !strings.HasPrefix(base, "file:") {
			base = "file:" + base
		}
		sep := "?"
		if strings.ContainsRune(base, '?') {
			sep = "&"
		}
		// mode=ro can't apply to a private in-memory DB (it would open a
		// distinct empty DB); rely on _query_only there.
		roDSN := base + sep + "_query_only=on&_busy_timeout=5000&_foreign_keys=on"
		if !strings.Contains(base, "mode=memory") && !strings.Contains(base, ":memory:") {
			roDSN += "&mode=ro"
		}
		conn, err := sql.Open("sqlite3", roDSN)
		if err != nil {
			h.roDBErr = err
			return
		}
		conn.SetMaxOpenConns(2)
		conn.SetMaxIdleConns(1)
		h.roDB = conn
	})
	return h.roDB, h.roDBErr
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *DatabaseHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// RegisterRoutes mounts the database endpoints. All DB-admin verbs
// require admin role — vacuum/checkpoint mutate WAL state, /db/query
// runs arbitrary SQL. Read-only stats are gated on admin too because
// the table-row counts surface internal record counts (envoys,
// secrets, audit chain entries) that aren't on any public surface.
func (h *DatabaseHandler) RegisterRoutes(mux *http.ServeMux) {
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/db/stats", admin(http.HandlerFunc(h.handleStats)))
	mux.Handle("GET /api/v1/db/integrity", admin(http.HandlerFunc(h.handleIntegrity)))
	mux.Handle("POST /api/v1/db/vacuum", admin(http.HandlerFunc(h.handleVacuum)))
	mux.Handle("POST /api/v1/db/checkpoint", admin(http.HandlerFunc(h.handleCheckpoint)))
	mux.Handle("POST /api/v1/db/query", admin(http.HandlerFunc(h.handleQuery)))
	mux.Handle("POST /api/v1/db/prune", admin(http.HandlerFunc(h.handlePrune)))
	mux.Handle("POST /api/v1/db/analyze", admin(http.HandlerFunc(h.handleAnalyze)))
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
		if h.sqlDB.QueryRow("PRAGMA page_count").Scan(&pageCount) == nil &&
			h.sqlDB.QueryRow("PRAGMA page_size").Scan(&pageSize) == nil {
			stats["page_count"] = pageCount
			stats["page_size"] = pageSize
		}

		// Free pages reusable in place. Non-zero is healthy slack, not a leak;
		// only a large freelist warrants an operator `db vacuum`.
		var freelistCount int64
		if h.sqlDB.QueryRow("PRAGMA freelist_count").Scan(&freelistCount) == nil {
			stats["freelist_count"] = freelistCount
		}
	}

	// Table row counts.
	tables, _ := h.tableRowCounts()
	stats["tables"] = tables

	writeJSON(w, r, http.StatusOK, stats)
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
				if err := rows.Scan(&table, &rowid, &parent, &fkidx); err != nil {
					continue
				}
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

	writeJSON(w, r, http.StatusOK, result)
}

// handleVacuum runs VACUUM to reclaim space and streams progress as
// newline-delimited JSON so the operator can see the operation isn't
// stuck. SQLite VACUUM doesn't expose a callback, so progress is
// approximated from the process's write_bytes counter against an
// expected ~2× DB-size budget (VACUUM reads the whole DB, then writes
// the whole DB). SQLite only.
func (h *DatabaseHandler) handleVacuum(w http.ResponseWriter, r *http.Request) {
	if h.driver != "sqlite" {
		writeError(w, http.StatusBadRequest, "VACUUM is only available for SQLite")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming response not supported")
		return
	}

	slog.Info("database VACUUM requested")
	startTime := time.Now()
	startWrite := readProcWriteBytes()

	var sizeBefore int64
	if info, err := os.Stat(h.dsn); err == nil {
		sizeBefore = info.Size()
	}
	expectedBytes := sizeBefore * 2 // VACUUM reads + writes the full DB

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx-style proxy buffering
	enc := json.NewEncoder(w)

	tick := func() {
		elapsed := time.Since(startTime)
		written := readProcWriteBytes() - startWrite
		var pct int64
		if expectedBytes > 0 {
			pct = written * 100 / expectedBytes
			if pct > 99 {
				pct = 99 // never claim 100% before the operation actually returns
			}
		}
		_ = enc.Encode(map[string]any{
			"status":         "running",
			"elapsed_ms":     elapsed.Milliseconds(),
			"written_bytes":  written,
			"expected_bytes": expectedBytes,
			"estimated_pct":  pct,
		})
		flusher.Flush()
	}

	// Send an immediate tick so the client sees something within milliseconds.
	tick()

	// Run VACUUM in a goroutine; main goroutine ticks until it returns.
	done := make(chan error, 1)
	go func() {
		_, err := h.sqlDB.Exec("VACUUM")
		done <- err
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			var sizeAfter int64
			if info, statErr := os.Stat(h.dsn); statErr == nil {
				sizeAfter = info.Size()
			}
			if err != nil {
				slog.Error("database VACUUM failed", "error", err, "elapsed", time.Since(startTime))
				_ = enc.Encode(map[string]any{
					"status":     "error",
					"error":      err.Error(),
					"elapsed_ms": time.Since(startTime).Milliseconds(),
				})
				flusher.Flush()
				return
			}
			slog.Info("database VACUUM complete",
				"before", sizeBefore, "after", sizeAfter, "reclaimed", sizeBefore-sizeAfter,
				"elapsed", time.Since(startTime))
			recordAudit(r.Context(), h.auditWriter, "db.vacuum", actorFromRequest(r), "rest",
				fmt.Sprintf("VACUUM reclaimed %d bytes in %s", sizeBefore-sizeAfter, time.Since(startTime).Truncate(time.Millisecond)), "")
			_ = enc.Encode(map[string]any{
				"status":     "done",
				"before":     sizeBefore,
				"after":      sizeAfter,
				"reclaimed":  sizeBefore - sizeAfter,
				"elapsed_ms": time.Since(startTime).Milliseconds(),
			})
			flusher.Flush()
			return
		case <-ticker.C:
			tick()
		case <-r.Context().Done():
			// Client gave up. VACUUM keeps running on the goroutine — we
			// can't cancel it cleanly. Just stop streaming.
			slog.Warn("database VACUUM client disconnected, server-side VACUUM continues")
			return
		}
	}
}

// readProcWriteBytes reports vigosrv's lifetime write bytes from
// /proc/self/io. Returns 0 on non-Linux or read errors; the caller
// uses deltas so absolute correctness doesn't matter.
func readProcWriteBytes() int64 {
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "write_bytes:") {
			var n int64
			fmt.Sscanf(strings.TrimPrefix(line, "write_bytes:"), "%d", &n)
			return n
		}
	}
	return 0
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

	recordAudit(r.Context(), h.auditWriter, "db.checkpoint", actorFromRequest(r), "rest",
		fmt.Sprintf("WAL checkpoint wrote %d of %d pages", pagesWritten, pagesTotal), "")

	writeJSON(w, r, http.StatusOK, map[string]any{
		"status":        "ok",
		"pages_total":   pagesTotal,
		"pages_written": pagesWritten,
	})
}

// handleAnalyze runs ANALYZE so the query planner refreshes its statistics.
// Cheap (seconds), worth running once after schema/index changes or after a
// bulk prune so the planner picks the right plan on the new shape.
func (h *DatabaseHandler) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	slog.Info("database ANALYZE requested")
	start := time.Now()
	if _, err := h.sqlDB.ExecContext(r.Context(), "ANALYZE"); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("ANALYZE failed: %v", err))
		return
	}
	elapsed := time.Since(start)
	slog.Info("database ANALYZE complete", "elapsed", elapsed)

	recordAudit(r.Context(), h.auditWriter, "db.analyze", actorFromRequest(r), "rest",
		fmt.Sprintf("ANALYZE completed in %s", elapsed.Truncate(time.Millisecond)), "")

	writeJSON(w, r, http.StatusOK, map[string]any{
		"status":     "ok",
		"elapsed_ms": elapsed.Milliseconds(),
	})
}

// handlePrune runs a one-shot retention prune at the requested age. Reuses
// the same query path as the periodic pruner so the cleanup matches the
// steady-state cadence.
func (h *DatabaseHandler) handlePrune(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OlderThan string `json:"older_than"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.OlderThan == "" {
		writeError(w, http.StatusBadRequest, "older_than is required (e.g. \"30d\", \"720h\")")
		return
	}
	dur, err := parsePruneDuration(req.OlderThan)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid older_than: %v", err))
		return
	}

	slog.Info("database prune requested", "older_than", req.OlderThan, "duration", dur)
	start := time.Now()

	pruner := db.NewRetentionPruner(h.queries, h.sqlDB, dur)
	pruner.SetPostgres(h.driver == "postgres")
	res := pruner.Prune(r.Context())

	recordAudit(r.Context(), h.auditWriter, "db.prune", actorFromRequest(r), "rest",
		fmt.Sprintf("pruned %d rows older than %s in %s", res.Total(), req.OlderThan, time.Since(start).Truncate(time.Millisecond)), "")

	writeJSON(w, r, http.StatusOK, map[string]any{
		"status":     "ok",
		"older_than": req.OlderThan,
		"deleted":    res.Deleted,
		"total":      res.Total(),
		"elapsed_ms": time.Since(start).Milliseconds(),
	})
}

// parsePruneDuration accepts Go duration strings (e.g. "720h") plus the
// "Nd" shorthand that the periodic pruner already accepts.
func parsePruneDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
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

	// Reject multiple statements outright — a single trailing/leading ";" is
	// fine, but interior semicolons let a "SELECT ...; DELETE ..." sneak a
	// write past the keyword prefix check. The read-only connection below is
	// the real guard; this keeps the error legible.
	if strings.Contains(strings.TrimRight(strings.TrimSpace(req.SQL), ";"), ";") {
		writeError(w, http.StatusBadRequest, "multiple statements are not allowed — submit a single SELECT")
		return
	}

	// Defense-in-depth keyword prefix denylist. The authoritative guard is the
	// query_only read-only connection acquired below; this prefix check stays
	// for a clearer error on the obvious cases (it is bypassable via CTEs).
	upper := strings.ToUpper(strings.TrimSpace(req.SQL))
	for _, prefix := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE", "ATTACH", "DETACH", "REINDEX", "VACUUM", "PRAGMA"} {
		if strings.HasPrefix(upper, prefix) {
			writeError(w, http.StatusForbidden, "write operations are not allowed — use read-only SELECT queries")
			return
		}
	}

	// Execute on a genuinely read-only connection (query_only=on) so a write
	// that slips past the denylist (WITH ... DELETE, etc.) fails at the engine.
	queryDB, err := h.readOnlyDB()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("opening read-only connection: %v", err))
		return
	}
	if queryDB == nil {
		queryDB = h.sqlDB // non-SQLite: fall back to shared handle + denylist
	}

	rows, err := queryDB.Query(req.SQL)
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

	writeJSON(w, r, http.StatusOK, map[string]any{
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
		if err := rows.Scan(&name); err != nil {
			continue
		}
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
		if h.sqlDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", name)).Scan(&count) == nil {
			tables[name] = count
		}
	}
	return tables, nil
}
