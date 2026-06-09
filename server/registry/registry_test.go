// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package registry_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/registry"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	var err error
	testDB, err = sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		log.Fatalf("failed to open sqlite: %v", err)
	}
	defer testDB.Close()

	// Enable WAL mode.
	testDB.Exec("PRAGMA journal_mode = WAL")

	// Run migrations.
	migrationsPath := findMigrationsDir()

	// Write the DB to a temp file so golang-migrate can use the sqlite3 scheme.
	// Actually, golang-migrate sqlite3 driver supports ":memory:" via the DSN.
	// But it opens its own connection, which won't share the in-memory DB.
	// Use a temp file instead.
	tmpDir, err := os.MkdirTemp("", "vigo-registry-test-*")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	testDB.Close()

	testDB, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
	if err != nil {
		log.Fatalf("failed to open sqlite file: %v", err)
	}
	defer testDB.Close()

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

// findMigrationsDir walks up from the test directory to find the migrations folder.
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

// newTestRegistry creates a fresh Registry backed by the test database.
// It deletes relevant rows before each test for isolation.
func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	ctx := context.Background()
	// Delete in correct order for foreign key constraints.
	for _, table := range []string{"run_results", "runs", "envoy_traits", "plans", "plan_results", "file_snapshots", "approved_patterns", "envoys"} {
		if _, err := testDB.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("failed to delete from %s: %v", table, err)
		}
	}
	queries := db.New(testDB)
	return registry.NewRegistry(queries)
}

func TestAddApprovedPattern(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patternID, err := reg.AddApprovedPattern(ctx, "*.prod.example.com", "my-secret-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}
	if patternID == "" {
		t.Fatal("expected non-empty pattern ID")
	}

	// Verify token is bcrypt-hashed (not stored plaintext)
	patterns, err := db.New(testDB).ListUnusedApprovedPatterns(ctx)
	if err != nil {
		t.Fatalf("ListUnusedApprovedPatterns failed: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].TokenHash == "my-secret-token" {
		t.Fatal("token stored as plaintext, expected bcrypt hash")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(patterns[0].TokenHash), []byte("my-secret-token")); err != nil {
		t.Fatalf("stored token hash does not match original token: %v", err)
	}
}

func TestGlobMatchHostname(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "test-token-123"
	_, err := reg.AddApprovedPattern(ctx, "*.prod.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// Should match
	clientID, err := reg.Register(ctx, "web01.prod.example.com", "10.0.1.5", "ssh-ed25519-pubkey-pem", token, false)
	if err != nil {
		t.Fatalf("Register should succeed for matching hostname: %v", err)
	}
	if clientID == "" {
		t.Fatal("expected non-empty client ID")
	}

	// Should NOT match staging
	_, err = reg.AddApprovedPattern(ctx, "*.staging.example.com", "another-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}
	_, err = reg.Register(ctx, "web01.prod.example.com", "10.0.2.5", "ssh-ed25519-pubkey-pem-2", "another-token", false)
	if err == nil {
		t.Fatal("Register should fail: hostname does not match *.staging.example.com pattern")
	}
}

func TestGlobMatchIP(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "ip-token-456"
	_, err := reg.AddApprovedPattern(ctx, "10.0.1.*", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// Should match IP
	clientID, err := reg.Register(ctx, "some-host", "10.0.1.5", "ssh-ed25519-pubkey-pem", token, false)
	if err != nil {
		t.Fatalf("Register should succeed for matching IP: %v", err)
	}
	if clientID == "" {
		t.Fatal("expected non-empty client ID")
	}
}

func TestRegisterSuccess(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "enroll-token"
	_, err := reg.AddApprovedPattern(ctx, "*.web.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	clientID, err := reg.Register(ctx, "app01.web.example.com", "192.168.1.10", "ed25519-pubkey", token, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if clientID == "" {
		t.Fatal("expected non-empty client ID")
	}

	// Verify envoy was created in DB
	envoy, err := db.New(testDB).GetEnvoyByID(ctx, clientID)
	if err != nil {
		t.Fatalf("GetEnvoyByID failed: %v", err)
	}
	if envoy.Hostname != "app01.web.example.com" {
		t.Errorf("hostname = %q, want %q", envoy.Hostname, "app01.web.example.com")
	}
	if envoy.IpAddress != "192.168.1.10" {
		t.Errorf("ip_address = %q, want %q", envoy.IpAddress, "192.168.1.10")
	}
	if envoy.PubkeyPem != "ed25519-pubkey" {
		t.Errorf("pubkey_pem = %q, want %q", envoy.PubkeyPem, "ed25519-pubkey")
	}
	if envoy.Revoked != 0 {
		t.Error("newly created envoy should not be revoked")
	}
}

func TestRegisterTokenBcrypt(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "bcrypt-test-token"
	_, err := reg.AddApprovedPattern(ctx, "*.secure.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// Register with correct token (bcrypt comparison, not plaintext)
	clientID, err := reg.Register(ctx, "node1.secure.example.com", "10.0.0.1", "pubkey1", token, false)
	if err != nil {
		t.Fatalf("Register with correct token should succeed: %v", err)
	}
	if clientID == "" {
		t.Fatal("expected non-empty client ID")
	}

	// Verify a wrong token is rejected (even with matching pattern)
	_, err = reg.AddApprovedPattern(ctx, "*.secure.example.com", "correct-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}
	_, err = reg.Register(ctx, "node2.secure.example.com", "10.0.0.2", "pubkey2", "wrong-token", false)
	if err == nil {
		t.Fatal("Register with wrong token should fail even if pattern matches")
	}
}

func TestRegisterTokenMarkedUsed(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "one-time-token"
	patternID, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	clientID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-web01", token, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify the pattern is now marked as used
	patterns, err := db.New(testDB).ListApprovedPatterns(ctx)
	if err != nil {
		t.Fatalf("ListApprovedPatterns failed: %v", err)
	}

	var found bool
	for _, p := range patterns {
		if p.ID == patternID {
			found = true
			if !p.UsedAt.Valid {
				t.Error("expected used_at to be set after registration")
			}
			if !p.UsedBy.Valid || p.UsedBy.String != clientID {
				t.Errorf("expected used_by = %s, got %v", clientID, p.UsedBy)
			}
		}
	}
	if !found {
		t.Fatal("pattern not found in ListApprovedPatterns")
	}
}

func TestRegisterTokenAlreadyUsed(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "single-use-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// First registration should succeed
	_, err = reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-1", token, false)
	if err != nil {
		t.Fatalf("First Register failed: %v", err)
	}

	// Second registration with same token should fail (one-time use)
	_, err = reg.Register(ctx, "web02.example.com", "10.0.1.2", "pubkey-2", token, false)
	if err == nil {
		t.Fatal("second registration with same token should fail (one-time token)")
	}
}

func TestRegisterNoMatchingPattern(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Add a pattern that won't match
	_, err := reg.AddApprovedPattern(ctx, "*.prod.example.com", "token123")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// Try to register with a hostname that doesn't match any pattern
	_, err = reg.Register(ctx, "rogue-host.evil.com", "192.168.0.1", "bad-pubkey", "token123", false)
	if err == nil {
		t.Fatal("Register should fail when hostname matches no pattern")
	}
}

func TestRegisterWrongToken(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	_, err := reg.AddApprovedPattern(ctx, "*.web.example.com", "correct-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// Correct hostname pattern, wrong token
	_, err = reg.Register(ctx, "app01.web.example.com", "10.0.1.1", "pubkey", "wrong-token", false)
	if err == nil {
		t.Fatal("Register should fail with wrong token even if pattern matches")
	}
}

func TestRevokeEnvoy(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a envoy
	token := "revoke-test-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	envoyID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke the envoy
	err = reg.RevokeEnvoy(ctx, envoyID)
	if err != nil {
		t.Fatalf("RevokeEnvoy failed: %v", err)
	}

	// Verify revocation state in DB
	envoy, err := db.New(testDB).GetEnvoyByID(ctx, envoyID)
	if err != nil {
		t.Fatalf("GetEnvoyByID failed: %v", err)
	}
	if envoy.Revoked == 0 {
		t.Error("expected envoy to be revoked")
	}
	if !envoy.RevokedAt.Valid {
		t.Error("expected revoked_at to be set")
	}
}

func TestRevokedNodeCheckIn(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a envoy
	token := "checkin-test-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	envoyID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke
	err = reg.RevokeEnvoy(ctx, envoyID)
	if err != nil {
		t.Fatalf("RevokeEnvoy failed: %v", err)
	}

	// ValidateEnvoy should return error for revoked envoy
	err = reg.ValidateEnvoy(ctx, envoyID)
	if err == nil {
		t.Fatal("ValidateEnvoy should return error for revoked envoy")
	}
}

func TestRevokedCannotReenroll(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a envoy
	token1 := "first-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token1)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	envoyID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-1", token1, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke
	err = reg.RevokeEnvoy(ctx, envoyID)
	if err != nil {
		t.Fatalf("RevokeEnvoy failed: %v", err)
	}

	// Try to re-enroll the same hostname (ENRL-07: revoked envoys cannot re-enroll)
	token2 := "second-token"
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", token2)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	_, err = reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-2", token2, false)
	if err == nil {
		t.Fatal("Register should fail for hostname of a revoked envoy (ENRL-07)")
	}
}

func TestValidateEnvoyNonExistent(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	err := reg.ValidateEnvoy(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("ValidateEnvoy should return error for non-existent envoy")
	}
}

func TestValidateEnvoyActive(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "active-envoy-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	envoyID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token, false)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Active envoy should validate successfully
	err = reg.ValidateEnvoy(ctx, envoyID)
	if err != nil {
		t.Fatalf("ValidateEnvoy should succeed for active envoy: %v", err)
	}
}

func TestRegisterMultiplePatternsFirstMatch(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Add multiple patterns with different tokens
	_, err := reg.AddApprovedPattern(ctx, "*.prod.example.com", "prod-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}
	_, err = reg.AddApprovedPattern(ctx, "10.0.1.*", "ip-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// Register with hostname matching first pattern
	clientID, err := reg.Register(ctx, "web01.prod.example.com", "192.168.1.1", "pubkey", "prod-token", false)
	if err != nil {
		t.Fatalf("Register should succeed with matching hostname pattern: %v", err)
	}
	if clientID == "" {
		t.Fatal("expected non-empty client ID")
	}
}

func TestGlobMatchIPNotMatch(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "ip-token"
	_, err := reg.AddApprovedPattern(ctx, "10.0.1.*", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	// IP 10.0.2.5 should NOT match 10.0.1.*
	_, err = reg.Register(ctx, "some-host", "10.0.2.5", "pubkey", token, false)
	if err == nil {
		t.Fatal("Register should fail when neither hostname nor IP matches pattern")
	}
}

func TestAddApprovedPatternErrorMessages(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Empty pattern should error
	_, err := reg.AddApprovedPattern(ctx, "", "token")
	if err == nil {
		t.Fatal("AddApprovedPattern should fail with empty pattern")
	}

	// Empty token should error
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", "")
	if err == nil {
		t.Fatal("AddApprovedPattern should fail with empty token")
	}
}

func TestRegisterErrorMessages(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Empty hostname
	_, err := reg.Register(ctx, "", "10.0.1.1", "pubkey", "token", false)
	if err == nil {
		t.Fatal("Register should fail with empty hostname")
	}

	// Empty token
	_, err = reg.Register(ctx, "host.example.com", "10.0.1.1", "pubkey", "", false)
	if err == nil {
		t.Fatal("Register should fail with empty token")
	}

	// Empty pubkey
	_, err = reg.Register(ctx, "host.example.com", "10.0.1.1", "", "token", false)
	if err == nil {
		t.Fatal("Register should fail with empty pubkey")
	}
}

func TestTrustedRegisterSuccess(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.web.example.com", CIDRs: []string{"10.0.1.0/24"}},
	}

	envoyID, err := reg.TrustedRegister(ctx, "app01.web.example.com", "10.0.1.5", "ed25519-pubkey", patterns, "10.0.1.100", false)
	if err != nil {
		t.Fatalf("TrustedRegister should succeed: %v", err)
	}
	if envoyID == "" {
		t.Fatal("expected non-empty envoy ID")
	}
}

func TestTrustedRegisterCIDRReject(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.web.example.com", CIDRs: []string{"10.0.1.0/24"}},
	}

	// Peer IP outside allowed CIDR.
	_, err := reg.TrustedRegister(ctx, "app01.web.example.com", "10.0.1.5", "ed25519-pubkey", patterns, "192.168.1.100", false)
	if err == nil {
		t.Fatal("TrustedRegister should reject when peer IP is outside CIDR")
	}
}

func TestTrustedRegisterNoCIDRsAllowsAny(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// No CIDRs means any source IP is allowed.
	patterns := []config.TrustedEnrollment{
		{Pattern: "*.open.example.com"},
	}

	envoyID, err := reg.TrustedRegister(ctx, "app01.open.example.com", "anywhere", "ed25519-pubkey", patterns, "203.0.113.50", false)
	if err != nil {
		t.Fatalf("TrustedRegister should succeed with no CIDR restriction: %v", err)
	}
	if envoyID == "" {
		t.Fatal("expected non-empty envoy ID")
	}
}

func TestTrustedRegisterNoMatch(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.web.example.com", CIDRs: []string{"10.0.0.0/8"}},
	}

	_, err := reg.TrustedRegister(ctx, "rogue-host.evil.com", "10.0.1.5", "ed25519-pubkey", patterns, "10.0.1.5", false)
	if err == nil {
		t.Fatal("TrustedRegister should reject when hostname doesn't match pattern")
	}
}

func TestTrustedRegisterRevokedHostname(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and revoke a envoy with token flow.
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", "tok")
	if err != nil {
		t.Fatal(err)
	}
	envoyID, err := reg.Register(ctx, "revoked.example.com", "10.0.1.1", "pk1", "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RevokeEnvoy(ctx, envoyID); err != nil {
		t.Fatal(err)
	}

	// Try to re-enroll the revoked hostname via trusted enrollment.
	patterns := []config.TrustedEnrollment{
		{Pattern: "*.example.com"},
	}
	_, err = reg.TrustedRegister(ctx, "revoked.example.com", "10.0.1.1", "pk2", patterns, "10.0.1.1", false)
	if err == nil {
		t.Fatal("TrustedRegister should reject revoked hostname")
	}
}

func TestRevokeNonExistentNode(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Revoking a non-existent envoy should not error (idempotent)
	// or could return an error -- depends on design. Testing behavior.
	err := reg.RevokeEnvoy(ctx, "00000000-0000-0000-0000-000000000000")
	// We accept either behavior but log it
	if err != nil {
		fmt.Printf("RevokeEnvoy for non-existent envoy returned error: %v (acceptable)\n", err)
	}
}

func TestBcryptHashFormat(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	_, err := reg.AddApprovedPattern(ctx, "*.example.com", "test-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}

	patterns, _ := db.New(testDB).ListUnusedApprovedPatterns(ctx)
	hash := patterns[0].TokenHash

	// Verify it's a valid bcrypt hash (starts with $2a$ or $2b$).
	if len(hash) < 4 || (hash[:4] != "$2a$" && hash[:4] != "$2b$") {
		t.Errorf("token hash doesn't look like bcrypt: %q", hash[:10])
	}

	// Verify cost is at least 10.
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost < 10 {
		t.Errorf("bcrypt cost = %d, want >= 10", cost)
	}
}

func TestRegisterEmptyInputs(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Empty hostname.
	_, err := reg.Register(ctx, "", "10.0.0.1", "pubkey", "token", false)
	if err == nil {
		t.Error("expected error for empty hostname")
	}

	// Empty pubkey.
	_, err = reg.Register(ctx, "host.example.com", "10.0.0.1", "", "token", false)
	if err == nil {
		t.Error("expected error for empty pubkey")
	}

	// Empty token.
	_, err = reg.Register(ctx, "host.example.com", "10.0.0.1", "pubkey", "", false)
	if err == nil {
		t.Error("expected error for empty token")
	}
}

func TestMatchesCIDR(t *testing.T) {
	patterns := []config.TrustedEnrollment{
		{Pattern: "*.example.com", CIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"}},
	}

	reg := newTestRegistry(t)
	ctx := context.Background()

	// Match: 10.x.x.x
	id, err := reg.TrustedRegister(ctx, "a.example.com", "10.0.0.1", "pk1", patterns, "10.0.0.50", false)
	if err != nil {
		t.Fatalf("expected match for 10.x: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	// Match: 172.16.x.x
	_, err = reg.TrustedRegister(ctx, "b.example.com", "172.16.0.1", "pk2", patterns, "172.16.5.5", false)
	if err != nil {
		t.Fatalf("expected match for 172.16.x: %v", err)
	}

	// No match: 192.168.x.x
	_, err = reg.TrustedRegister(ctx, "c.example.com", "192.168.1.1", "pk3", patterns, "192.168.1.100", false)
	if err == nil {
		t.Fatal("expected rejection for 192.168.x outside CIDR")
	}

	// Invalid CIDR format should not crash.
	badPatterns := []config.TrustedEnrollment{
		{Pattern: "*.example.com", CIDRs: []string{"not-a-cidr"}},
	}
	_, err = reg.TrustedRegister(ctx, "d.example.com", "10.0.0.1", "pk4", badPatterns, "10.0.0.1", false)
	if err == nil {
		t.Fatal("expected rejection with invalid CIDR")
	}
}

func TestRevokeNonExistentEnvoyReturnsError(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	err := reg.RevokeEnvoy(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected error when revoking non-existent envoy")
	}
}

func TestCreateEnvoyDirect(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	id, err := reg.CreateEnvoy(ctx, "direct.example.com", "10.0.0.1", "ed25519-pubkey", false)
	if err != nil {
		t.Fatalf("CreateEnvoy: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	// Verify in DB.
	envoy, err := db.New(testDB).GetEnvoyByID(ctx, id)
	if err != nil {
		t.Fatalf("GetEnvoyByID: %v", err)
	}
	if envoy.Hostname != "direct.example.com" {
		t.Errorf("hostname = %q, want direct.example.com", envoy.Hostname)
	}
}

func TestRegisterDuplicateHostnameRejected(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Register a envoy with token-based flow.
	token1 := "dup-token-1"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token1)
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	_, err = reg.Register(ctx, "dup.example.com", "10.0.0.1", "pubkey-1", token1, false)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Attempt to register another envoy with the same hostname (different pubkey, new pattern+token).
	token2 := "dup-token-2"
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", token2)
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	_, err = reg.Register(ctx, "dup.example.com", "10.0.0.2", "pubkey-2", token2, false)
	if !errors.Is(err, registry.ErrHostnameInUse) {
		t.Fatalf("expected ErrHostnameInUse, got: %v", err)
	}
}

func TestRegisterDuplicateHostnameForce(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Register the first envoy.
	token1 := "force-token-1"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token1)
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	oldID, err := reg.Register(ctx, "force.example.com", "10.0.0.1", "pubkey-old", token1, false)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Force-register with the same hostname.
	token2 := "force-token-2"
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", token2)
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	newID, err := reg.Register(ctx, "force.example.com", "10.0.0.2", "pubkey-new", token2, true)
	if err != nil {
		t.Fatalf("force Register: %v", err)
	}
	if newID == oldID {
		t.Fatal("expected new envoy to have a different ID from the old one")
	}

	// Verify the old envoy is revoked.
	oldEnvoy, err := db.New(testDB).GetEnvoyByID(ctx, oldID)
	if err != nil {
		t.Fatalf("GetEnvoyByID (old): %v", err)
	}
	if oldEnvoy.Revoked == 0 {
		t.Error("expected old envoy to be revoked after force registration")
	}
}

func TestRegisterForceRevokedHostname(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Register and then revoke a envoy.
	token1 := "revoke-force-token-1"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token1)
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	envoyID, err := reg.Register(ctx, "revforce.example.com", "10.0.0.1", "pubkey-1", token1, false)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.RevokeEnvoy(ctx, envoyID); err != nil {
		t.Fatalf("RevokeEnvoy: %v", err)
	}

	// Without force, re-enrolling a revoked hostname fails (ENRL-07).
	token2 := "revoke-force-token-2"
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", token2)
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	_, err = reg.Register(ctx, "revforce.example.com", "10.0.0.2", "pubkey-2", token2, false)
	if err == nil {
		t.Fatal("expected error re-enrolling revoked hostname without force")
	}

	// With force, re-enrolling should succeed (force overrides ENRL-07).
	token3 := "revoke-force-token-3"
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", token3)
	if err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	newID, err := reg.Register(ctx, "revforce.example.com", "10.0.0.3", "pubkey-3", token3, true)
	if err != nil {
		t.Fatalf("force Register over revoked hostname should succeed: %v", err)
	}
	if newID == "" {
		t.Fatal("expected non-empty envoy ID")
	}
}

func TestTrustedRegisterDuplicateHostnameRejected(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.example.com", CIDRs: []string{"10.0.0.0/8"}},
	}

	// Register first envoy via trusted enrollment.
	_, err := reg.TrustedRegister(ctx, "trusted-dup.example.com", "10.0.0.1", "pubkey-1", patterns, "10.0.0.50", false)
	if err != nil {
		t.Fatalf("first TrustedRegister: %v", err)
	}

	// Second registration with same hostname should be rejected.
	_, err = reg.TrustedRegister(ctx, "trusted-dup.example.com", "10.0.0.2", "pubkey-2", patterns, "10.0.0.51", false)
	if !errors.Is(err, registry.ErrHostnameInUse) {
		t.Fatalf("expected ErrHostnameInUse, got: %v", err)
	}
}

func TestTrustedRegisterDuplicateHostnameForce(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.example.com", CIDRs: []string{"10.0.0.0/8"}},
	}

	// Register first envoy.
	oldID, err := reg.TrustedRegister(ctx, "trusted-force.example.com", "10.0.0.1", "pubkey-old", patterns, "10.0.0.50", false)
	if err != nil {
		t.Fatalf("first TrustedRegister: %v", err)
	}

	// Force-register with same hostname.
	newID, err := reg.TrustedRegister(ctx, "trusted-force.example.com", "10.0.0.2", "pubkey-new", patterns, "10.0.0.51", true)
	if err != nil {
		t.Fatalf("force TrustedRegister: %v", err)
	}
	if newID == oldID {
		t.Fatal("expected new envoy to have a different ID from the old one")
	}

	// Verify old envoy is revoked.
	oldEnvoy, err := db.New(testDB).GetEnvoyByID(ctx, oldID)
	if err != nil {
		t.Fatalf("GetEnvoyByID (old): %v", err)
	}
	if oldEnvoy.Revoked == 0 {
		t.Error("expected old envoy to be revoked after force trusted registration")
	}
}

func TestCreateEnvoyDuplicateHostnameRejected(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create first envoy directly.
	_, err := reg.CreateEnvoy(ctx, "create-dup.example.com", "10.0.0.1", "pubkey-1", false)
	if err != nil {
		t.Fatalf("first CreateEnvoy: %v", err)
	}

	// Second CreateEnvoy with same hostname should be rejected.
	_, err = reg.CreateEnvoy(ctx, "create-dup.example.com", "10.0.0.2", "pubkey-2", false)
	if !errors.Is(err, registry.ErrHostnameInUse) {
		t.Fatalf("expected ErrHostnameInUse, got: %v", err)
	}
}

// --- License enrollment gate tests ---

// stubGate is a minimal EnrollmentGate for tests. It records every call and
// can be configured to accept, reject, or fail closed on either the count
// read or the policy check.
//
// count is the mode-aware count source the gate reports via CurrentCount —
// modeling the enforcer's EnvoyCounter. newRegistryWithGate defaults it to the
// wired FleetIndex's ActiveCount (local-mode parity, what every gate test
// other than the aggregate-divergence one wants); a test can override it to
// report a fleet-wide count divergent from the local fleet (aggregate mode).
type stubGate struct {
	maxAllowed   int        // reject if currentCount >= maxAllowed (set to -1 to always allow)
	count        func() int // mode-aware count source; defaulted to fi.ActiveCount by the helper
	errorOnCount bool       // if true, CurrentCount fails (counter unavailable)
	errorOnCheck bool       // if true, RegistrationAllowedAt fails (gate internal error)

	countCalls    int
	calls         int
	blockedCalls  int
	errorCalls    int
	rejectedCount int // count passed on the most recent rejection
}

func (s *stubGate) CurrentCount(_ context.Context) (int, error) {
	s.countCalls++
	if s.errorOnCount {
		return 0, fmt.Errorf("stub: count unavailable")
	}
	if s.count != nil {
		return s.count(), nil
	}
	return 0, nil
}

func (s *stubGate) RegistrationAllowedAt(currentCount int) error {
	s.calls++
	if s.errorOnCheck {
		return fmt.Errorf("stub: count unavailable")
	}
	if s.maxAllowed >= 0 && currentCount >= s.maxAllowed {
		s.rejectedCount = currentCount
		return fmt.Errorf("stub: node limit reached (%d/%d)", currentCount, s.maxAllowed)
	}
	return nil
}

func (s *stubGate) RecordEnforcementError()  { s.errorCalls++ }
func (s *stubGate) RecordEnrollmentBlocked() { s.blockedCalls++ }

func newRegistryWithGate(t *testing.T, gate *stubGate) (*registry.Registry, *fleet.FleetIndex) {
	t.Helper()
	reg := newTestRegistry(t)
	fi := fleet.NewFleetIndex()
	if gate.count == nil {
		gate.count = fi.ActiveCount // local-mode parity: gate counts the live fleet
	}
	reg.SetFleetIndex(fi)
	reg.SetEnrollmentGate(gate)
	return reg, fi
}

// TestCreateEnvoy_GateAllowsBelowLimit verifies the gate is consulted and
// enrollment proceeds when the live count is below the configured limit.
func TestCreateEnvoy_GateAllowsBelowLimit(t *testing.T) {
	gate := &stubGate{maxAllowed: 5}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	envoyID, err := reg.CreateEnvoy(ctx, "gate-below.example.com", "10.0.0.1", "pubkey-1", false)
	if err != nil {
		t.Fatalf("CreateEnvoy: %v", err)
	}
	if envoyID == "" {
		t.Fatal("expected non-empty envoy id")
	}
	if gate.calls != 1 {
		t.Errorf("expected 1 gate call, got %d", gate.calls)
	}
	if fi.ActiveCount() != 1 {
		t.Errorf("expected fleet ActiveCount=1, got %d", fi.ActiveCount())
	}
	if gate.blockedCalls != 0 {
		t.Errorf("expected no blocked calls, got %d", gate.blockedCalls)
	}
}

// TestCreateEnvoy_GateBlocksAtLimit verifies the hard gate rejects when the
// live count is at the configured limit, and that RecordEnrollmentBlocked is
// called so the metric is incremented.
func TestCreateEnvoy_GateBlocksAtLimit(t *testing.T) {
	gate := &stubGate{maxAllowed: 3}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	// Fill to the limit.
	for i := 0; i < 3; i++ {
		_, err := reg.CreateEnvoy(ctx, fmt.Sprintf("gate-fill-%d.example.com", i), "10.0.0.1", fmt.Sprintf("pk-%d", i), false)
		if err != nil {
			t.Fatalf("CreateEnvoy #%d: %v", i, err)
		}
	}
	if fi.ActiveCount() != 3 {
		t.Fatalf("expected fleet ActiveCount=3, got %d", fi.ActiveCount())
	}

	// Next enrollment should be rejected by the gate.
	_, err := reg.CreateEnvoy(ctx, "gate-over.example.com", "10.0.0.1", "pk-over", false)
	if err == nil {
		t.Fatal("expected gate rejection at count==max")
	}
	if !strings.Contains(err.Error(), "node limit") {
		t.Errorf("expected error to mention node limit, got: %v", err)
	}
	if fi.ActiveCount() != 3 {
		t.Errorf("ActiveCount should be unchanged after rejection, got %d", fi.ActiveCount())
	}
	if gate.blockedCalls != 1 {
		t.Errorf("expected 1 RecordEnrollmentBlocked call, got %d", gate.blockedCalls)
	}
}

// TestCreateEnvoy_GateFailsClosedOnError verifies that a gate that returns an
// error (e.g., counter unavailable) blocks the enrollment — no envoy created,
// no fleet mutation.
func TestCreateEnvoy_GateFailsClosedOnError(t *testing.T) {
	gate := &stubGate{maxAllowed: 100, errorOnCheck: true}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	_, err := reg.CreateEnvoy(ctx, "fail-closed.example.com", "10.0.0.1", "pk-fc", false)
	if err == nil {
		t.Fatal("expected failure when gate errors")
	}
	if fi.ActiveCount() != 0 {
		t.Errorf("expected no fleet additions on gate error, got ActiveCount=%d", fi.ActiveCount())
	}
}

// TestCreateEnvoy_GateEnforcesModeAwareCount is the regression guard for the
// aggregate-license enforcement fix: the registry must source the count from
// the gate's mode-aware CurrentCount, NOT its own local FleetIndex. Here the
// gate reports a fleet-wide count at the limit while this bolt's local fleet is
// empty (the spanner aggregate-mode shape) — enrollment must still be rejected.
// Before the fix the registry passed fleet.ActiveCount() (0) and would have let
// the enrollment through, defeating the fleet-wide cap.
func TestCreateEnvoy_GateEnforcesModeAwareCount(t *testing.T) {
	gate := &stubGate{maxAllowed: 100, count: func() int { return 100 }}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	if fi.ActiveCount() != 0 {
		t.Fatalf("precondition: local fleet should be empty, got %d", fi.ActiveCount())
	}

	_, err := reg.CreateEnvoy(ctx, "agg-over.example.com", "10.0.0.9", "pk-agg", false)
	if err == nil {
		t.Fatal("expected rejection: fleet-wide count is at the limit even though the local fleet is empty")
	}
	if !strings.Contains(err.Error(), "node limit") {
		t.Errorf("expected node-limit rejection, got: %v", err)
	}
	if gate.countCalls != 1 {
		t.Errorf("expected CurrentCount consulted exactly once, got %d", gate.countCalls)
	}
	if gate.rejectedCount != 100 {
		t.Errorf("gate should have been handed the fleet-wide count 100, got %d", gate.rejectedCount)
	}
	if gate.blockedCalls != 1 {
		t.Errorf("expected 1 RecordEnrollmentBlocked, got %d", gate.blockedCalls)
	}
	if fi.ActiveCount() != 0 {
		t.Errorf("no envoy should be created on rejection, got %d", fi.ActiveCount())
	}
}

// TestCreateEnvoy_GateFailsClosedOnCountError verifies that when the mode-aware
// count source itself errors (e.g. the aggregate cache is unavailable), the
// registry fails closed: no policy check, no envoy, an enforcement error
// recorded. The count read and the policy check are distinct failure surfaces.
func TestCreateEnvoy_GateFailsClosedOnCountError(t *testing.T) {
	gate := &stubGate{maxAllowed: 5, errorOnCount: true}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	_, err := reg.CreateEnvoy(ctx, "count-err.example.com", "10.0.0.1", "pk-ce", false)
	if err == nil {
		t.Fatal("expected failure when the count source errors")
	}
	if !strings.Contains(err.Error(), "count unavailable") {
		t.Errorf("expected count-unavailable error, got: %v", err)
	}
	if gate.calls != 0 {
		t.Errorf("RegistrationAllowedAt must not run when the count read fails, got %d calls", gate.calls)
	}
	if gate.errorCalls != 1 {
		t.Errorf("expected 1 RecordEnforcementError, got %d", gate.errorCalls)
	}
	if fi.ActiveCount() != 0 {
		t.Errorf("no envoy should be created on count error, got %d", fi.ActiveCount())
	}
}

// TestCreateEnvoy_NoFleetIndexFailsClosed verifies the registry reports an
// enforcement error and rejects enrollment when a gate is wired but the
// FleetIndex is not — the gate cannot run without a live count.
func TestCreateEnvoy_NoFleetIndexFailsClosed(t *testing.T) {
	gate := &stubGate{maxAllowed: 5}
	reg := newTestRegistry(t)
	reg.SetEnrollmentGate(gate) // gate set, but no fleet index
	ctx := context.Background()

	_, err := reg.CreateEnvoy(ctx, "no-fi.example.com", "10.0.0.1", "pk-no-fi", false)
	if err == nil {
		t.Fatal("expected failure when fleet index is not wired")
	}
	if !strings.Contains(err.Error(), "fleet index") {
		t.Errorf("expected error to mention fleet index, got: %v", err)
	}
	if gate.errorCalls != 1 {
		t.Errorf("expected 1 RecordEnforcementError, got %d", gate.errorCalls)
	}
}

// TestForceReplace_AtLimit verifies that --force replacement succeeds even
// when the fleet is at the node limit: the revoke-old step runs before the
// gate check, so the gate sees the decremented count and the net enrollment
// is zero. This is the critical correctness property for machine replacement.
func TestForceReplace_AtLimit(t *testing.T) {
	gate := &stubGate{maxAllowed: 3}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	// Fill to the limit.
	for i := 0; i < 3; i++ {
		_, err := reg.CreateEnvoy(ctx, fmt.Sprintf("force-host-%d.example.com", i), "10.0.0.1", fmt.Sprintf("pk-%d", i), false)
		if err != nil {
			t.Fatalf("CreateEnvoy #%d: %v", i, err)
		}
	}
	if fi.ActiveCount() != 3 {
		t.Fatalf("expected ActiveCount=3, got %d", fi.ActiveCount())
	}

	// Without --force, next enrollment of an existing hostname hits the
	// "already in use" check before the gate runs.
	_, err := reg.CreateEnvoy(ctx, "force-host-0.example.com", "10.0.0.2", "pk-replacement", false)
	if !errors.Is(err, registry.ErrHostnameInUse) {
		t.Errorf("expected ErrHostnameInUse without --force, got: %v", err)
	}

	// With --force at the limit, the revoke happens first, the gate sees
	// count=2, and the new envoy slots in. Net-zero.
	newID, err := reg.CreateEnvoy(ctx, "force-host-0.example.com", "10.0.0.2", "pk-replacement", true)
	if err != nil {
		t.Fatalf("force-replace at limit should succeed, got: %v", err)
	}
	if newID == "" {
		t.Fatal("expected non-empty new envoy id")
	}
	if fi.ActiveCount() != 3 {
		t.Errorf("expected ActiveCount=3 after net-zero replacement, got %d", fi.ActiveCount())
	}
	if gate.blockedCalls != 0 {
		t.Errorf("expected no blocked calls for force-replace at limit, got %d", gate.blockedCalls)
	}
}

// TestConcurrentEnrollments_NoDoubleBook verifies the registry's serialization
// mutex prevents two parallel enrollments from both passing the gate at the
// boundary. Fires N concurrent CreateEnvoy calls against a registry with a
// hard limit; exactly `limit` should succeed and the rest should fail.
// This is the TOCTOU regression guard.
func TestConcurrentEnrollments_NoDoubleBook(t *testing.T) {
	const limit = 5
	const racers = 20

	gate := &stubGate{maxAllowed: limit}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	var wg sync.WaitGroup
	var successes int64
	var failures int64
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(idx int) {
			defer wg.Done()
			_, err := reg.CreateEnvoy(ctx, fmt.Sprintf("racer-%d.example.com", idx), "10.0.0.1", fmt.Sprintf("pk-%d", idx), false)
			if err == nil {
				atomic.AddInt64(&successes, 1)
			} else {
				atomic.AddInt64(&failures, 1)
			}
		}(i)
	}
	wg.Wait()

	if successes != limit {
		t.Errorf("expected exactly %d successes, got %d (failures=%d)", limit, successes, failures)
	}
	if failures != racers-limit {
		t.Errorf("expected exactly %d failures, got %d", racers-limit, failures)
	}
	if fi.ActiveCount() != limit {
		t.Errorf("expected ActiveCount=%d after race, got %d", limit, fi.ActiveCount())
	}
}

// TestRevokeEnvoy_DecrementsCount verifies that explicit revocation through
// the registry decrements the FleetIndex active count, freeing a slot for a
// new enrollment.
func TestRevokeEnvoy_DecrementsCount(t *testing.T) {
	gate := &stubGate{maxAllowed: 2}
	reg, fi := newRegistryWithGate(t, gate)
	ctx := context.Background()

	id1, err := reg.CreateEnvoy(ctx, "revoke-1.example.com", "10.0.0.1", "pk-1", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.CreateEnvoy(ctx, "revoke-2.example.com", "10.0.0.2", "pk-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ActiveCount() != 2 {
		t.Fatalf("expected ActiveCount=2, got %d", fi.ActiveCount())
	}

	// Third should be blocked.
	if _, err := reg.CreateEnvoy(ctx, "revoke-3.example.com", "10.0.0.3", "pk-3", false); err == nil {
		t.Fatal("expected rejection at limit")
	}

	// Revoke the first envoy — fleet count goes back to 1.
	if err := reg.RevokeEnvoy(ctx, id1); err != nil {
		t.Fatalf("RevokeEnvoy: %v", err)
	}
	if fi.ActiveCount() != 1 {
		t.Errorf("expected ActiveCount=1 after revoke, got %d", fi.ActiveCount())
	}

	// Third enrollment now fits.
	if _, err := reg.CreateEnvoy(ctx, "revoke-3.example.com", "10.0.0.3", "pk-3", false); err != nil {
		t.Fatalf("expected enrollment to succeed after revoke, got: %v", err)
	}
}
