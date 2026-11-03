// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/metrics"
)

func TestLicense_Status(t *testing.T) {
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// LoadDir falls back to the built-in free license.
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 5, nil // 5 active envoys out of 100
	}, nil, metrics.NewTestMetrics())
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

func TestLicense_StatusRedactsForNonAdmin(t *testing.T) {
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// Built-in free license — CustomerName "Community", LicenseID "free-100".
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 5, nil
	}, nil, metrics.NewTestMetrics())
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}

	handler := NewLicenseHandler(enforcer)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	get := func(wrap func(*http.Request) *http.Request) license.Status {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/license", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, wrap(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var status license.Status
		if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return status
	}

	// Admin sees the full identity — and there must be something to redact,
	// otherwise the viewer assertions below would pass vacuously.
	admin := get(adminReq)
	if admin.License == nil {
		t.Fatal("admin: expected a license payload")
	}
	if admin.License.CustomerName == "" || admin.License.LicenseID == "" {
		t.Fatalf("admin: expected populated identity, got name=%q id=%q",
			admin.License.CustomerName, admin.License.LicenseID)
	}

	// Viewer (fleet.read, not admin) gets operational fields but no identity.
	viewer := get(viewerReq)
	if viewer.License == nil {
		t.Fatal("viewer: expected a license payload")
	}
	if viewer.License.CustomerName != "" {
		t.Errorf("viewer: customer_name not redacted: %q", viewer.License.CustomerName)
	}
	if viewer.License.CustomerID != "" {
		t.Errorf("viewer: customer_id not redacted: %q", viewer.License.CustomerID)
	}
	if viewer.License.LicenseID != "" {
		t.Errorf("viewer: license_id not redacted: %q", viewer.License.LicenseID)
	}
	if viewer.License.Fingerprint != "" {
		t.Errorf("viewer: fingerprint not redacted: %q", viewer.License.Fingerprint)
	}
	// Operational fields survive redaction.
	if viewer.License.Tier != admin.License.Tier {
		t.Errorf("viewer: tier altered: got %q want %q", viewer.License.Tier, admin.License.Tier)
	}
	if viewer.MaxEnvoys != admin.MaxEnvoys {
		t.Errorf("viewer: max_envoys altered: got %d want %d", viewer.MaxEnvoys, admin.MaxEnvoys)
	}
}

func TestLicense_StatusOverLimit(t *testing.T) {
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// 120 envoys exceeds the free 100 limit — should be in grace.
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 120, nil
	}, nil, metrics.NewTestMetrics())
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
