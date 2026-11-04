// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/registry"
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

	// FleetIndex is now owned by the registry — insertEnvoyLocked calls
	// fi.AddEnvoy under the enrollment mutex so the gate check and the
	// index mutation are atomic.
	fi := fleet.NewFleetIndex()
	reg.SetFleetIndex(fi)

	// Set up handler with CA + registry + FleetIndex (handler still holds
	// a reference for post-enrollment capacity reporting in the response).
	h := newTestHandlerWithCA(t, dir)
	h.SetRegistry(reg)
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

	fi := fleet.NewFleetIndex()
	reg.SetFleetIndex(fi)

	h := newTestHandlerWithCA(t, dir)
	h.SetRegistry(reg)
	h.SetTrustedPatterns([]config.TrustedEnrollment{
		{Pattern: "*.example.com", CIDRs: []string{"0.0.0.0/0"}},
	})
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

// trustedRegisterReq posts a token-less (trusted) enrollment for hostname
// from remoteAddr against a handler that trusts every peer.
func trustedRegisterReq(t *testing.T, h *Handler, hostname, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	csr := generateTestCSR(t, "x")
	body := fmt.Sprintf(`{"hostname":%s,"ip_address":"10.0.2.10","pubkey_pem":"fakepk","csr_pem":%s}`, mustJSON(hostname), mustJSON(string(csr)))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func newTrustedHandler(t *testing.T) *Handler {
	t.Helper()
	cleanDB(t)
	reg := registry.NewRegistry(db.New(testDB))
	h := newTestHandlerWithCA(t, t.TempDir())
	h.SetRegistry(reg)
	h.SetTrustedPatterns([]config.TrustedEnrollment{{Pattern: "*"}})
	return h
}

// An envoy cert carries ServerAuth from the server's own CA, so a remote
// enrollee must not be able to take one of the server's TLS names. The
// loopback server-self envoy, which shares the server hostname, still can.
func TestBootstrapRegister_ReservedServerName(t *testing.T) {
	h := newTrustedHandler(t)
	h.SetReservedNames([]string{"vigo.example.com", "localhost"})

	w := trustedRegisterReq(t, h, "VIGO.example.com", "10.0.2.10:5555")
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote enrollment as a server name: want 403, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "https://127.0.0.1:8443/bootstrap") {
		t.Errorf("reserved-name refusal does not point at loopback enrollment: %q", w.Body.String())
	}
	if w := trustedRegisterReq(t, h, "localhost", "10.0.2.10:5555"); w.Code != http.StatusForbidden {
		t.Fatalf("remote enrollment as localhost: want 403, got %d: %s", w.Code, w.Body.String())
	}
	if w := trustedRegisterReq(t, h, "vigo.example.com", "127.0.0.1:5555"); w.Code != http.StatusOK {
		t.Fatalf("loopback server-self enrollment: want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// Wildcard / IP-literal / malformed hostnames are refused before anything
// else runs.
func TestBootstrapRegister_InvalidHostname(t *testing.T) {
	h := newTrustedHandler(t)
	for _, name := range []string{"*.example.com", "10.0.0.5", "bad_host"} {
		if w := trustedRegisterReq(t, h, name, "10.0.2.10:5555"); w.Code != http.StatusBadRequest {
			t.Errorf("hostname %q: want 400, got %d: %s", name, w.Code, w.Body.String())
		}
	}
}

// A failed registration tells the unauthenticated caller nothing about the
// registry's internal state.
func TestBootstrapRegister_DeniedIsGeneric(t *testing.T) {
	cleanDB(t)
	reg := registry.NewRegistry(db.New(testDB))
	h := newTestHandlerWithCA(t, t.TempDir())
	h.SetRegistry(reg)
	h.SetTrustedPatterns([]config.TrustedEnrollment{{Pattern: "*.example.com", CIDRs: []string{"10.9.0.0/16"}}})

	w := trustedRegisterReq(t, h, "web-01.example.com", "10.0.2.10:5555")
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != "registration denied" {
		t.Fatalf("want generic body %q, got %q", "registration denied", got)
	}
}

// stubSpannerRouter is a test double for the spanner enrollment router so the
// REST routing path can be exercised without standing up a second bolt.
type stubSpannerRouter struct {
	ownerHostname string
	ownsLocally   bool
	boltEnvoyID   string
	enrollErr     error
	enrolledCount int
	gotCertSPKI   string // the pin the routed enrollment carried
}

func (s *stubSpannerRouter) OwningBolt(context.Context, string) (string, string, bool) {
	return "ownerpub", s.ownerHostname, s.ownerHostname != ""
}
func (s *stubSpannerRouter) OwnsLocally(string) bool { return s.ownsLocally }
func (s *stubSpannerRouter) EnrollOnPeerBolt(_ context.Context, _, _, _, _, certSPKI string, _ bool) (string, error) {
	s.enrolledCount++
	s.gotCertSPKI = certSPKI
	return s.boltEnvoyID, s.enrollErr
}
func (s *stubSpannerRouter) GRPCPort() string { return "1530" }

// TestBootstrapRegister_SpannerRouting verifies the REST bootstrap path routes
// an envoy to the bolt whose patterns own its hostname (the gap that left
// spanner partitioning non-functional for install-script enrollments), and
// fails open to local registration when the peer enroll errors.
func TestBootstrapRegister_SpannerRouting(t *testing.T) {
	cases := []struct {
		name         string
		router       *stubSpannerRouter
		wantBoltAddr string
		wantBoltID   bool // EnvoyID should be the bolt-assigned id
		wantEnrolled int
	}{
		{"routes to owning bolt", &stubSpannerRouter{ownerHostname: "bolt-2", boltEnvoyID: "bolt2-id"}, "bolt-2:1530", true, 1},
		{"fallback on enroll error", &stubSpannerRouter{ownerHostname: "bolt-2", boltEnvoyID: "bolt2-id", enrollErr: fmt.Errorf("peer down")}, "", false, 1},
		{"this bolt owns it", &stubSpannerRouter{ownerHostname: "bolt-1", ownsLocally: true}, "", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanDB(t)
			ctx := context.Background()
			dir := t.TempDir()
			queries := db.New(testDB)
			reg := registry.NewRegistry(queries)
			if _, err := reg.AddApprovedPattern(ctx, "*.example.com", "test-token"); err != nil {
				t.Fatalf("AddApprovedPattern: %v", err)
			}
			fi := fleet.NewFleetIndex()
			reg.SetFleetIndex(fi)
			h := newTestHandlerWithCA(t, dir)
			h.SetRegistry(reg)
			h.SetFleetIndex(fi)
			h.SetSpannerRouter(tc.router)

			csr := generateTestCSR(t, "web-01.example.com")
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			body := fmt.Sprintf(`{"hostname":"web-01.example.com","ip_address":"10.0.1.5","pubkey_pem":"fakepk","csr_pem":%s,"token":"test-token"}`, mustJSON(string(csr)))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
			}
			var resp bootstrapRegisterResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.BoltAddr != tc.wantBoltAddr {
				t.Errorf("BoltAddr = %q, want %q", resp.BoltAddr, tc.wantBoltAddr)
			}
			if tc.router.enrolledCount != tc.wantEnrolled {
				t.Errorf("EnrollOnPeerBolt calls = %d, want %d", tc.router.enrolledCount, tc.wantEnrolled)
			}
			// A routed enrollment carries the signed cert's pin to the owning
			// bolt; it used to arrive unpinned.
			if tc.wantEnrolled > 0 && len(tc.router.gotCertSPKI) != 64 {
				t.Errorf("routed enrollment carried cert pin %q, want the signed cert's 64-hex SPKI pin", tc.router.gotCertSPKI)
			}
			if tc.wantBoltID {
				if resp.EnvoyID != "bolt2-id" {
					t.Errorf("EnvoyID = %q, want bolt-assigned bolt2-id", resp.EnvoyID)
				}
				// Routed away: the local FleetIndex entry must be dropped.
				if fi.GetEnvoy(resp.EnvoyID) != nil {
					t.Error("routed envoy should not be in the local FleetIndex")
				}
			} else {
				if resp.EnvoyID == "bolt2-id" {
					t.Error("EnvoyID should be the local id, not the bolt's")
				}
				// Local registration stands.
				if fi.GetEnvoy(resp.EnvoyID) == nil {
					t.Error("local envoy should be in the FleetIndex")
				}
			}
		})
	}
}

// TestBootstrapRegister_BadCSRCreatesNoEnvoy: the CSR used to be signed after
// the envoy row was created, so a malformed CSR failed the request but left
// a cert-less envoy registered. A bad CSR must be refused with no row.
func TestBootstrapRegister_BadCSRCreatesNoEnvoy(t *testing.T) {
	h := newTrustedHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	body := `{"hostname":"bad-csr.example.com","ip_address":"10.0.2.10","pubkey_pem":"fakepk","csr_pem":"-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
	req.RemoteAddr = "10.0.2.10:5555"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad CSR: want 400, got %d: %s", w.Code, w.Body.String())
	}
	var n int
	if err := testDB.QueryRow("SELECT COUNT(*) FROM envoys WHERE hostname = ?", "bad-csr.example.com").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("bad CSR left %d envoy row(s) behind", n)
	}
}
