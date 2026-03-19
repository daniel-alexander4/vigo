// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackup_SnapshotRequiresAdmin(t *testing.T) {
	handler := NewBackupHandler("", "", "sqlite", ":memory:", "test", "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// No auth context — RequireAdmin middleware should block with 403.
	req := httptest.NewRequest("POST", "/api/v1/backup/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBackup_DownloadRequiresAdmin(t *testing.T) {
	handler := NewBackupHandler("", "", "sqlite", ":memory:", "test", "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// No auth context — RequireAdmin middleware should block with 403.
	req := httptest.NewRequest("GET", "/api/v1/backup/download", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBackup_SnapshotWithAdmin(t *testing.T) {
	// With a valid server root, snapshot creates an archive. We'll point it
	// at a temp dir so it doesn't touch real paths. This tests that the handler
	// gets past auth and attempts the backup (which may fail on missing files,
	// but at least exercises the plumbing).
	dir := t.TempDir()
	handler := NewBackupHandler(dir+"/server.yaml", dir, "sqlite", dir+"/hobnob.db", "test", "none")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := requestWithUser("POST", "/api/v1/backup/snapshot", nil, "admin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// The backup may succeed (200) or fail (500) depending on whether the
	// temp dir has the expected structure, but it should not be 403.
	if rec.Code == http.StatusForbidden {
		t.Fatal("admin should not be blocked by auth middleware")
	}
}
