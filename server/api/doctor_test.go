// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/daniel-alexander4/vigo/server/secrets"
)

// openDoctorTestDB creates an in-memory SQLite database with the envoys table.
func openDoctorTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	_, err = sqlDB.Exec(`
		CREATE TABLE envoys (
			id                       TEXT PRIMARY KEY,
			hostname                 TEXT NOT NULL,
			ip_address               TEXT NOT NULL,
			pubkey_pem               TEXT NOT NULL,
			enrolled_at              DATETIME NOT NULL DEFAULT (datetime('now')),
			last_seen                DATETIME,
			revoked                  INTEGER NOT NULL DEFAULT 0,
			revoked_at               DATETIME,
			force_push               INTEGER NOT NULL DEFAULT 0,
			pending_rotated_secrets  TEXT NOT NULL DEFAULT '[]',
			tags                     TEXT NOT NULL DEFAULT '',
			maintenance_until        DATETIME,
			agent_version            TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return sqlDB, db.New(sqlDB)
}

// generateTestCerts creates a temporary TLS cert+key pair valid for 365 days.
func generateTestCerts(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	os.WriteFile(certFile, certPEM, 0644)
	os.WriteFile(keyFile, keyPEM, 0600)
	return
}

// newTestLicenseEnforcer creates a license enforcer with the built-in free license.
func newTestLicenseEnforcer(t *testing.T) *license.Enforcer {
	t.Helper()
	licenseDir := t.TempDir()
	graceDir := t.TempDir()

	// LoadDir falls back to the built-in free license when no .lic files exist.
	enforcer, err := license.NewEnforcer(licenseDir, graceDir, "", func(_ context.Context) (int, error) {
		return 0, nil // 0 active envoys
	}, nil)
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	return enforcer
}

// newDoctorTestConfigStore creates a minimal ConfigStore for doctor tests.
func newDoctorTestConfigStore(t *testing.T) *config.ConfigStore {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"envoys", "modules", "roles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	// Write an empty envoys.vgo
	os.WriteFile(filepath.Join(dir, "envoys", "envoys.vgo"), []byte("envoys: []\n"), 0644)

	provider, err := secrets.NewLocalProvider("", t.TempDir())
	if err != nil {
		t.Fatalf("new local provider: %v", err)
	}
	t.Cleanup(func() { provider.Close() })

	store, err := config.NewConfigStore(dir, provider, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	return store
}

func TestDoctor_Healthy(t *testing.T) {
	sqlDB, queries := openDoctorTestDB(t)
	certFile, keyFile := generateTestCerts(t)
	enforcer := newTestLicenseEnforcer(t)
	configStore := newDoctorTestConfigStore(t)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		Enforcer:    enforcer,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp DoctorResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	// Overall should be healthy or degraded (not unhealthy).
	if resp.Status == "unhealthy" {
		t.Errorf("expected healthy or degraded, got unhealthy")
		for _, c := range resp.Checks {
			if c.Status == StatusFail {
				t.Logf("  FAIL: %s — %s", c.Name, c.Message)
			}
		}
	}

	// Database should pass.
	foundDB := false
	for _, c := range resp.Checks {
		if c.Name == "database" {
			foundDB = true
			if c.Status != StatusPass {
				t.Errorf("expected database check to pass, got %s: %s", c.Status, c.Message)
			}
		}
	}
	if !foundDB {
		t.Error("database check not found in response")
	}

	// TLS should pass (valid cert).
	for _, c := range resp.Checks {
		if c.Name == "tls" && c.Status != StatusPass {
			t.Errorf("expected tls check to pass, got %s: %s", c.Status, c.Message)
		}
	}
}

func TestDoctor_BadTLSCert(t *testing.T) {
	sqlDB, queries := openDoctorTestDB(t)
	enforcer := newTestLicenseEnforcer(t)
	configStore := newDoctorTestConfigStore(t)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    "/nonexistent/cert.pem",
		KeyFile:     "/nonexistent/key.pem",
		Enforcer:    enforcer,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp DoctorResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Status != "unhealthy" {
		t.Errorf("expected unhealthy with bad certs, got %s", resp.Status)
	}

	for _, c := range resp.Checks {
		if c.Name == "tls" && c.Status != StatusFail {
			t.Errorf("expected tls check to fail, got %s", c.Status)
		}
	}
}

func TestDoctor_StaleEnvoys(t *testing.T) {
	sqlDB, queries := openDoctorTestDB(t)
	certFile, keyFile := generateTestCerts(t)
	enforcer := newTestLicenseEnforcer(t)
	configStore := newDoctorTestConfigStore(t)

	// Insert a envoy that was last seen 30 minutes ago (stale with 5min interval).
	staleTime := time.Now().Add(-30 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	_, err := sqlDB.Exec(
		`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem, last_seen) VALUES (?, ?, ?, ?, ?)`,
		"t-stale", "stale.host", "10.0.0.1", "pubkey", staleTime,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		Enforcer:    enforcer,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute, // 2*5min=10min threshold
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp DoctorResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	for _, c := range resp.Checks {
		if c.Name == "stale_envoys" && c.Status != StatusWarn {
			t.Errorf("expected stale_envoys check to warn, got %s: %s", c.Status, c.Message)
		}
	}
}
