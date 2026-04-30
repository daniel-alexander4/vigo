// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/license"
)

func TestLicense_Status(t *testing.T) {
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// LoadDir falls back to the built-in free license.
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 5, nil // 5 active envoys out of 100
	}, nil)
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}

	handler := NewLicenseHandler(enforcer)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/license", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var status license.Status
	json.NewDecoder(rec.Body).Decode(&status)

	if status.Stage != license.StageCompliant {
		t.Errorf("expected StageCompliant, got %v", status.Stage)
	}
	if status.MaxEnvoys != 100 {
		t.Errorf("expected max_envoys 100, got %d", status.MaxEnvoys)
	}
	if status.ActiveEnvoys != 5 {
		t.Errorf("expected active_envoys 5, got %d", status.ActiveEnvoys)
	}
}

func TestLicense_StatusOverLimit(t *testing.T) {
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// 120 envoys exceeds the free 100 limit — should be in grace.
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 120, nil
	}, nil)
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}

	handler := NewLicenseHandler(enforcer)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/license", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var status license.Status
	json.NewDecoder(rec.Body).Decode(&status)

	if status.Stage != license.StageGrace {
		t.Errorf("expected StageGrace for over-limit, got %v", status.Stage)
	}
	if status.Violation == "" {
		t.Error("expected violation message for over-limit")
	}
}
