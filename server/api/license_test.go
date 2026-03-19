// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hobnob/hobnob/server/license"
)

func TestLicense_Status(t *testing.T) {
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// LoadDir falls back to the built-in free license.
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 5, nil // 5 active thralls out of 25
	}, nil)
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}

	handler := NewLicenseHandler(enforcer)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/license", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var status license.Status
	json.NewDecoder(rec.Body).Decode(&status)

	if status.Stage != license.StageCompliant {
		t.Errorf("expected StageCompliant, got %v", status.Stage)
	}
	if status.MaxThralls != 25 {
		t.Errorf("expected max_thralls 25, got %d", status.MaxThralls)
	}
	if status.ActiveThralls != 5 {
		t.Errorf("expected active_thralls 5, got %d", status.ActiveThralls)
	}
}

func TestLicense_StatusOverLimit(t *testing.T) {
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// 30 thralls exceeds the free 25 limit — should be in grace.
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 30, nil
	}, nil)
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}

	handler := NewLicenseHandler(enforcer)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/license", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

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
