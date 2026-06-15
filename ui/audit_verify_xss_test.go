// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/audit"
	"git.alexander4.io/vigo/server/db"

	_ "github.com/mattn/go-sqlite3"
)

// TestAuditVerify_EscapesEntryContent proves the AuditVerify fragment escapes
// operator-influenceable audit fields (actor, summary) and the source field
// that lands inside a class attribute — a crafted value must not inject markup
// into the hand-built HTML.
func TestAuditVerify_EscapesEntryContent(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", "file:auditxsstest?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`
		CREATE TABLE audit_entries (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			prev_hash   TEXT NOT NULL DEFAULT '',
			hash        TEXT NOT NULL,
			timestamp   DATETIME NOT NULL DEFAULT (datetime('now')),
			event_type  TEXT NOT NULL,
			actor       TEXT NOT NULL DEFAULT '',
			source      TEXT NOT NULL DEFAULT '',
			summary     TEXT NOT NULL DEFAULT '',
			payload     TEXT NOT NULL DEFAULT '{}',
			hash_version INTEGER NOT NULL DEFAULT 1
		);`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	queries := db.New(sqlDB)

	// Write a real, hash-chained entry through the audit writer so VerifyFull
	// reports a valid chain and AuditVerify renders the entry table.
	w := audit.NewWriter(queries)
	const (
		evilActor   = `<script>alert('xss-actor')</script>`
		evilSummary = `<img src=x onerror=alert('xss-summary')>`
		evilSource  = `evil"><b>break`
	)
	if err := w.Record(context.Background(), "test.event", evilActor, evilSource, evilSummary, "{}"); err != nil {
		t.Fatalf("record audit entry: %v", err)
	}

	h := &Handler{queries: queries}
	rec := httptest.NewRecorder()
	h.AuditVerify(rec, httptest.NewRequest(http.MethodGet, "/ui/fragments/audit-verify", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Raw payloads must never appear unescaped.
	for _, raw := range []string{evilActor, evilSummary, `evil"><b>`} {
		if strings.Contains(body, raw) {
			t.Errorf("response contains unescaped payload %q — XSS not prevented", raw)
		}
	}
	// And the escaped form must be present, proving the entry rendered (valid
	// chain → table) rather than the payload simply being absent.
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("response missing escaped actor markup; body=%s", body)
	}
}
