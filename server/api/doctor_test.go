// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/secrets"
)

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

// newDoctorTestConfigStore creates a minimal ConfigStore for doctor tests.
func newDoctorTestConfigStore(t *testing.T) *config.ConfigStore {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"stacks/modules", "stacks/envoys", "stacks/roles"} {
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

// newUnhealthyConfigStore builds a ConfigStore whose initial loadAll
// produces an IdempotencyErrors (unguarded `exec` resource with
// `dangerous=false`). The store comes back non-nil with empty config
// and the error stashed in LastReloadError() — the same shape the
// status endpoint and doctor check are wired to detect.
func newUnhealthyConfigStore(t *testing.T) *config.ConfigStore {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"configcrates", "envoys", "roles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	os.WriteFile(filepath.Join(dir, "configcrates", "bad.vgo"), []byte(`
name: bad
resources:
  - name: yolo-exec
    type: exec
    command: "echo hello"
`), 0644)
	os.WriteFile(filepath.Join(dir, "envoys", "envoys.vgo"), []byte(`
envoys:
  - match: "test.example.com"
    configcrates: [bad]
`), 0644)

	provider, err := secrets.NewLocalProvider("", t.TempDir())
	if err != nil {
		t.Fatalf("new local provider: %v", err)
	}
	t.Cleanup(func() { provider.Close() })

	store, err := config.NewConfigStore(dir, provider, false) // dangerous=false → unguarded exec rejects
	if err != nil {
		t.Fatalf("NewConfigStore should return non-nil store with stashed error, got: %v", err)
	}
	if store.LastReloadError() == nil {
		t.Fatal("expected LastReloadError to be set after unguarded-exec load")
	}
	return store
}

func TestDoctor_Healthy(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
	configStore := newDoctorTestConfigStore(t)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	configStore := newDoctorTestConfigStore(t)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    "/nonexistent/cert.pem",
		KeyFile:     "/nonexistent/key.pem",
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

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

func TestDoctor_CheckScopes(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
	configStore := newDoctorTestConfigStore(t)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
	})

	_, checks := handler.RunChecks()

	wantScope := map[string]CheckScope{
		"database":             ScopeServer,
		"tls":                  ScopeServer,
		"secrets":              ScopeServer,
		"spanner":              ScopeServer,
		"integrations":         ScopeServer,
		"config":               ScopeServer,
		"swarm_pattern_order":  ScopeServer,
		"stale_envoys":         ScopeFleet,
		"unpinned_envoys":      ScopeFleet,
		"webusers_replication": ScopeServer,
	}
	for _, c := range checks {
		want, ok := wantScope[c.Name]
		if !ok {
			t.Errorf("unexpected check %q", c.Name)
			continue
		}
		if c.Scope != want {
			t.Errorf("check %q: scope = %q, want %q", c.Name, c.Scope, want)
		}
	}
	if len(checks) != len(wantScope) {
		t.Errorf("got %d checks, want %d", len(checks), len(wantScope))
	}
}

func TestDoctor_StaleEnvoys(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
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
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute, // 2*5min=10min threshold
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp DoctorResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	for _, c := range resp.Checks {
		if c.Name == "stale_envoys" && c.Status != StatusWarn {
			t.Errorf("expected stale_envoys check to warn, got %s: %s", c.Status, c.Message)
		}
	}
}

// TestDoctor_UnpinnedEnvoys: a non-revoked envoy with no enrolled cert pin is
// reported (by hostname, with the re-enroll command); pinned and revoked
// envoys are not.
func TestDoctor_UnpinnedEnvoys(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
	for _, row := range []struct {
		id, host, pin string
		revoked       int
	}{
		{"t-unpinned", "unpinned.host", "", 0},
		{"t-pinned", "pinned.host", "abc123", 0},
		{"t-revoked", "revoked.host", "", 1},
	} {
		if _, err := sqlDB.Exec(
			`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem, cert_spki_sha256, revoked) VALUES (?, ?, ?, ?, ?, ?)`,
			row.id, row.host, "10.0.0.1", "pubkey", row.pin, row.revoked,
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	handler := NewDoctorHandler(DoctorConfig{
		SQLDB: sqlDB, CertFile: certFile, KeyFile: keyFile,
		ConfigStore: newDoctorTestConfigStore(t), Queries: queries, Interval: 5 * time.Minute,
	})
	_, checks := handler.RunChecks()
	var got *DoctorCheck
	for i := range checks {
		if checks[i].Name == "unpinned_envoys" {
			got = &checks[i]
		}
	}
	if got == nil {
		t.Fatal("unpinned_envoys check missing")
	}
	if got.Status != StatusWarn {
		t.Fatalf("unpinned_envoys = %s (%s), want warn", got.Status, got.Message)
	}
	for _, want := range []string{"1 envoy(s)", "unpinned.host", "curl -sSfk https://<server>:8443/bootstrap | sudo sh"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message %q missing %q", got.Message, want)
		}
	}
	for _, not := range []string{"pinned.host,", "revoked.host"} {
		if strings.Contains(got.Message, not) {
			t.Errorf("message %q names %q", got.Message, not)
		}
	}
}

func TestDoctor_ConfigReloadFailure(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
	configStore := newUnhealthyConfigStore(t)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp DoctorResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	var found bool
	for _, c := range resp.Checks {
		if c.Name == "config" {
			found = true
			if c.Status != StatusFail {
				t.Errorf("expected config check to FAIL on reload error, got %s: %s", c.Status, c.Message)
			}
			if !strings.Contains(c.Message, "reload failed") {
				t.Errorf("expected message to mention 'reload failed', got: %s", c.Message)
			}
		}
	}
	if !found {
		t.Fatal("config check not found in response")
	}
}

// TestDoctor_WebusersReplication_FlagsUnsigned pins the ADR-030
// Slice 2-iii-f doctor check: in spanner mode with unsigned legacy
// rows present, the check warns with the count.
func TestDoctor_WebusersReplication_FlagsUnsigned(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
	configStore := newDoctorTestConfigStore(t)
	userStore := auth.NewUserStore(queries)

	// Seed an unsigned legacy user (no signer wired).
	_, err := userStore.Create(context.Background(), db.InsertUserParams{Username: "legacy-1", Role: "viewer"})
	require.NoError(t, err)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
		Mode:        config.ModeSpanner,
		UserStore:   userStore,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp DoctorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	check := findCheck(t, resp.Checks, "webusers_replication")
	if check.Status != StatusWarn {
		t.Errorf("expected Warn for unsigned legacy users, got %s: %s", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "awaiting first-replication") {
		t.Errorf("expected message to mention awaiting first-replication, got: %s", check.Message)
	}
}

// TestDoctor_WebusersReplication_AllSigned_Pass pins the happy
// post-startup state: every row signed, check passes.
func TestDoctor_WebusersReplication_AllSigned_Pass(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
	configStore := newDoctorTestConfigStore(t)
	userStore := auth.NewUserStore(queries)
	// No users at all → CountUnsignedUsers = 0 → Pass.

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
		Mode:        config.ModeSpanner,
		UserStore:   userStore,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp DoctorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	check := findCheck(t, resp.Checks, "webusers_replication")
	if check.Status != StatusPass {
		t.Errorf("expected Pass when no unsigned users, got %s: %s", check.Status, check.Message)
	}
}

// TestDoctor_WebusersReplication_Standalone_Skip pins that the
// check skips in standalone mode — no signer, every row would
// always be unsigned, warn would be permanent noise.
func TestDoctor_WebusersReplication_Standalone_Skip(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)
	certFile, keyFile := generateTestCerts(t)
	configStore := newDoctorTestConfigStore(t)
	userStore := auth.NewUserStore(queries)

	// Seed an unsigned legacy user — would Warn in spanner mode but
	// must be skipped in standalone.
	_, err := userStore.Create(context.Background(), db.InsertUserParams{Username: "legacy-1", Role: "viewer"})
	require.NoError(t, err)

	handler := NewDoctorHandler(DoctorConfig{
		SQLDB:       sqlDB,
		CertFile:    certFile,
		KeyFile:     keyFile,
		ConfigStore: configStore,
		Queries:     queries,
		Interval:    5 * time.Minute,
		Mode:        config.ModeStandalone,
		UserStore:   userStore,
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	var resp DoctorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	check := findCheck(t, resp.Checks, "webusers_replication")
	if check.Status != StatusSkip {
		t.Errorf("expected Skip in standalone mode, got %s: %s", check.Status, check.Message)
	}
}

func findCheck(t *testing.T, checks []DoctorCheck, name string) DoctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in response", name)
	return DoctorCheck{}
}
