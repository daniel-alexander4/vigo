// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/registry"
)

var testDB *sql.DB
var testDBDir string

func TestMain(m *testing.M) {
	var err error
	testDBDir, err = os.MkdirTemp("", "vigo-bootstrap-test-*")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(testDBDir)

	dbPath := filepath.Join(testDBDir, "test.db")
	testDB, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
	if err != nil {
		log.Fatalf("failed to open sqlite: %v", err)
	}
	defer testDB.Close()
	testDB.Exec("PRAGMA journal_mode = WAL")

	migrationsPath := findMigrationsDir()
	mig, err := migrate.New("file://"+migrationsPath, "sqlite3://"+dbPath)
	if err != nil {
		log.Fatalf("failed to create migrate instance: %v", err)
	}
	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("failed to run migrations: %v", err)
	}
	mig.Close()

	os.Exit(m.Run())
}

func findMigrationsDir() string {
	dir, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			log.Fatal("could not find migrations directory")
		}
		dir = parent
	}
}

func cleanDB(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"run_results", "runs", "envoy_traits", "plans", "plan_results", "file_snapshots", "approved_patterns", "envoys"} {
		if _, err := testDB.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("failed to delete from %s: %v", table, err)
		}
	}
}

func TestBootstrapRegister_HappyPath_PopulatesFleetIndex(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()
	dir := t.TempDir()

	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Create an approved pattern + token for enrollment.
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", "test-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}

	// Set up handler with CA + registry + FleetIndex.
	h := newTestHandlerWithCA(t, dir)
	h.SetRegistry(reg)

	fi := fleet.NewFleetIndex()
	h.SetFleetIndex(fi)

	// Generate a CSR for the test hostname.
	csr := generateTestCSR(t, "web-01.example.com")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := fmt.Sprintf(`{"hostname":"web-01.example.com","ip_address":"10.0.1.5","pubkey_pem":"fakepk","csr_pem":%s,"token":"test-token"}`, mustJSON(string(csr)))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		respBody := w.Body.String()
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var respBody bootstrapRegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if respBody.EnvoyID == "" {
		t.Fatal("expected non-empty envoy_id")
	}
	if respBody.SignedCertPEM == "" {
		t.Fatal("expected non-empty signed_cert_pem")
	}
	if respBody.CACertPEM == "" {
		t.Fatal("expected non-empty ca_cert_pem")
	}

	// Verify the envoy is in FleetIndex — this is the bug that was missing.
	ts := fi.GetEnvoy(respBody.EnvoyID)
	if ts == nil {
		t.Fatal("envoy not found in FleetIndex after bootstrap registration")
	}
	if ts.Hostname != "web-01.example.com" {
		t.Errorf("expected hostname web-01.example.com, got %s", ts.Hostname)
	}
	if ts.IP != "10.0.1.5" {
		t.Errorf("expected IP 10.0.1.5, got %s", ts.IP)
	}

	// Also verify hostname lookup works (this is what check-in uses).
	id := fi.GetByHostname("web-01.example.com")
	if id != respBody.EnvoyID {
		t.Errorf("FleetIndex hostname lookup returned %q, expected %q", id, respBody.EnvoyID)
	}
}

func TestBootstrapRegister_TrustedEnrollment_PopulatesFleetIndex(t *testing.T) {
	cleanDB(t)
	dir := t.TempDir()

	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	h := newTestHandlerWithCA(t, dir)
	h.SetRegistry(reg)
	h.SetTrustedPatterns([]config.TrustedEnrollment{
		{Pattern: "*.example.com", CIDRs: []string{"0.0.0.0/0"}},
	})

	fi := fleet.NewFleetIndex()
	h.SetFleetIndex(fi)

	csr := generateTestCSR(t, "db-01.example.com")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// No token — uses trusted enrollment.
	body := fmt.Sprintf(`{"hostname":"db-01.example.com","ip_address":"10.0.2.10","pubkey_pem":"fakepk","csr_pem":%s}`, mustJSON(string(csr)))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
	req.RemoteAddr = "10.0.2.10:54321"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, w.Body.String())
	}

	var respBody bootstrapRegisterResponse
	json.NewDecoder(resp.Body).Decode(&respBody)

	ts := fi.GetEnvoy(respBody.EnvoyID)
	if ts == nil {
		t.Fatal("envoy not found in FleetIndex after trusted bootstrap registration")
	}
	if ts.Hostname != "db-01.example.com" {
		t.Errorf("expected hostname db-01.example.com, got %s", ts.Hostname)
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
