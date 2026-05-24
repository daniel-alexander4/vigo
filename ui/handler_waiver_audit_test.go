// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.alexander4.io/vigo/server/db"

	_ "github.com/mattn/go-sqlite3"
)

// fakeAuditWriter captures Record calls so tests can assert on
// eventType / actor / source / summary. Matches the auditRecorder
// interface in ui/audit.go via structural typing.
type fakeAuditWriter struct {
	mu     sync.Mutex
	events []fakeAuditEvent
}

type fakeAuditEvent struct {
	EventType string
	Actor     string
	Source    string
	Summary   string
	Payload   string
}

func (f *fakeAuditWriter) Record(_ context.Context, eventType, actor, source, summary, payload string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fakeAuditEvent{eventType, actor, source, summary, payload})
	return nil
}

func (f *fakeAuditWriter) only() fakeAuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) != 1 {
		panic("expected exactly one recorded event")
	}
	return f.events[0]
}

// openWaiverTestDB creates an in-memory sqlite database with just the
// compliance_waivers table the audit-logging tests need. Mirrors the
// shared-cache file-URI pattern used by openDatabaseTestDB in
// server/api/database_test.go so the same connection pool semantics
// apply.
func openWaiverTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", "file:waivertest?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		DROP TABLE IF EXISTS compliance_waivers;
		CREATE TABLE compliance_waivers (
			id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(8)))),
			framework   TEXT NOT NULL,
			control_id  TEXT NOT NULL,
			reason      TEXT NOT NULL,
			approver    TEXT NOT NULL,
			created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
			expires_at  DATETIME,
			active      INTEGER NOT NULL DEFAULT 1
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

func TestCreateWaiver_EmitsAuditEvent(t *testing.T) {
	_, queries := openWaiverTestDB(t)
	aw := &fakeAuditWriter{}
	h := &Handler{queries: queries, auditWriter: aw}

	body := strings.NewReader(`{"framework":"hipaa","control_id":"AC-2","reason":"legacy system, upgrade Q4","approver":"alice"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/waivers", body)
	rec := httptest.NewRecorder()

	h.CreateWaiver(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	ev := aw.only()
	if ev.EventType != "compliance.waiver_created" {
		t.Errorf("event type: got %q, want compliance.waiver_created", ev.EventType)
	}
	if ev.Actor != "alice" {
		t.Errorf("actor: got %q, want alice (from request approver, no session user)", ev.Actor)
	}
	if ev.Source != "ui" {
		t.Errorf("source: got %q, want ui", ev.Source)
	}
	for _, want := range []string{"hipaa", "AC-2", "legacy system"} {
		if !strings.Contains(ev.Summary, want) {
			t.Errorf("summary %q must contain %q", ev.Summary, want)
		}
	}
}

func TestDeleteWaiver_EmitsAuditEventWithFrameworkAndControl(t *testing.T) {
	_, queries := openWaiverTestDB(t)
	aw := &fakeAuditWriter{}
	h := &Handler{queries: queries, auditWriter: aw}

	// Seed a waiver via the create path, then capture its id from the
	// emitted JSON so the delete request can reference it.
	createBody := strings.NewReader(`{"framework":"pci-dss","control_id":"2.1","reason":"compensating control","approver":"bob"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/waivers", createBody)
	createRec := httptest.NewRecorder()
	h.CreateWaiver(createRec, createReq)

	if createRec.Code != http.StatusOK {
		t.Fatalf("seed CreateWaiver: %d %s", createRec.Code, createRec.Body.String())
	}
	// Extract id from the JSON response (waiver struct serialized).
	var resp map[string]any
	if err := decodeJSON(createRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	id, _ := resp["ID"].(string)
	if id == "" {
		id, _ = resp["id"].(string)
	}
	if id == "" {
		t.Fatalf("no id in create response: %v", resp)
	}

	// Reset the audit log so we only inspect the delete event.
	aw.events = nil

	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/compliance/waivers/"+id, nil)
	delReq.SetPathValue("id", id)
	delRec := httptest.NewRecorder()
	h.DeleteWaiver(delRec, delReq)

	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}
	ev := aw.only()
	if ev.EventType != "compliance.waiver_revoked" {
		t.Errorf("event type: got %q, want compliance.waiver_revoked", ev.EventType)
	}
	for _, want := range []string{"pci-dss", "2.1"} {
		if !strings.Contains(ev.Summary, want) {
			t.Errorf("summary %q must name framework + control_id, missing %q", ev.Summary, want)
		}
	}
}

func TestDeleteWaiver_NotFoundDoesNotEmitAudit(t *testing.T) {
	// An unknown id should 404 before the audit row is written — no
	// "revoked nothing" rows in the chain.
	_, queries := openWaiverTestDB(t)
	aw := &fakeAuditWriter{}
	h := &Handler{queries: queries, auditWriter: aw}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/compliance/waivers/missing", nil)
	req.SetPathValue("id", "missing")
	rec := httptest.NewRecorder()
	h.DeleteWaiver(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if len(aw.events) != 0 {
		t.Errorf("expected no audit events on not-found, got %d", len(aw.events))
	}
}

func TestCreateWaiver_NilAuditWriterIsNoOp(t *testing.T) {
	// auditWriter == nil must not panic — the helper guards.
	_, queries := openWaiverTestDB(t)
	h := &Handler{queries: queries} // auditWriter intentionally nil

	body := strings.NewReader(`{"framework":"soc2","control_id":"CC6.1","reason":"x","approver":"carol"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/waivers", body)
	rec := httptest.NewRecorder()
	h.CreateWaiver(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// decodeJSON unmarshals into the target. Tiny helper to keep the test
// body readable.
func decodeJSON(data []byte, target any) error {
	return json.NewDecoder(bytes.NewReader(data)).Decode(target)
}
