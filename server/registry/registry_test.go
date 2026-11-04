// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

// --- Fleet-index consistency tests ---

func newRegistryWithFleet(t *testing.T) (*registry.Registry, *fleet.FleetIndex) {
	t.Helper()
	reg := newTestRegistry(t)
	fi := fleet.NewFleetIndex()
	reg.SetFleetIndex(fi)
	return reg, fi
}

// TestCreateEnvoy_NoNodeLimit verifies enrollment has no node cap: well past
// the former 100-node community limit, every enrollment succeeds and the
// fleet index tracks each one.
func TestCreateEnvoy_NoNodeLimit(t *testing.T) {
	const n = 150
	reg, fi := newRegistryWithFleet(t)
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := reg.CreateEnvoy(ctx, fmt.Sprintf("nolimit-%d.example.com", i), "10.0.0.1", fmt.Sprintf("pk-%d", i), false); err != nil {
			t.Fatalf("CreateEnvoy #%d: %v", i, err)
		}
	}
	if fi.ActiveCount() != n {
		t.Errorf("expected ActiveCount=%d, got %d", n, fi.ActiveCount())
	}
}

// TestForceReplace_NetZero verifies --force revokes the old envoy before the
// new one is inserted, so the fleet index count is unchanged.
func TestForceReplace_NetZero(t *testing.T) {
	reg, fi := newRegistryWithFleet(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := reg.CreateEnvoy(ctx, fmt.Sprintf("force-host-%d.example.com", i), "10.0.0.1", fmt.Sprintf("pk-%d", i), false); err != nil {
			t.Fatalf("CreateEnvoy #%d: %v", i, err)
		}
	}

	_, err := reg.CreateEnvoy(ctx, "force-host-0.example.com", "10.0.0.2", "pk-replacement", false)
	if !errors.Is(err, registry.ErrHostnameInUse) {
		t.Errorf("expected ErrHostnameInUse without --force, got: %v", err)
	}

	newID, err := reg.CreateEnvoy(ctx, "force-host-0.example.com", "10.0.0.2", "pk-replacement", true)
	if err != nil {
		t.Fatalf("force-replace should succeed, got: %v", err)
	}
	if newID == "" {
		t.Fatal("expected non-empty new envoy id")
	}
	if fi.ActiveCount() != 3 {
		t.Errorf("expected ActiveCount=3 after net-zero replacement, got %d", fi.ActiveCount())
	}
}

// TestConcurrentEnrollments_IndexConsistent verifies the registry's
// serialization mutex keeps the fleet index in step with the DB under
// parallel enrollments: every racer succeeds and the index counts each once.
func TestConcurrentEnrollments_IndexConsistent(t *testing.T) {
	const racers = 20
	reg, fi := newRegistryWithFleet(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	var failures int64
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(idx int) {
			defer wg.Done()
			if _, err := reg.CreateEnvoy(ctx, fmt.Sprintf("racer-%d.example.com", idx), "10.0.0.1", fmt.Sprintf("pk-%d", idx), false); err != nil {
				atomic.AddInt64(&failures, 1)
			}
		}(i)
	}
	wg.Wait()

	if failures != 0 {
		t.Errorf("expected no failures, got %d", failures)
	}
	if fi.ActiveCount() != racers {
		t.Errorf("expected ActiveCount=%d after race, got %d", racers, fi.ActiveCount())
	}
}

// TestRevokeEnvoy_DecrementsCount verifies that explicit revocation through
// the registry decrements the FleetIndex active count.
func TestRevokeEnvoy_DecrementsCount(t *testing.T) {
	reg, fi := newRegistryWithFleet(t)
	ctx := context.Background()

	id1, err := reg.CreateEnvoy(ctx, "revoke-1.example.com", "10.0.0.1", "pk-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.CreateEnvoy(ctx, "revoke-2.example.com", "10.0.0.2", "pk-2", false); err != nil {
		t.Fatal(err)
	}
	if fi.ActiveCount() != 2 {
		t.Fatalf("expected ActiveCount=2, got %d", fi.ActiveCount())
	}
	if err := reg.RevokeEnvoy(ctx, id1); err != nil {
		t.Fatalf("RevokeEnvoy: %v", err)
	}
	if fi.ActiveCount() != 1 {
		t.Errorf("expected ActiveCount=1 after revoke, got %d", fi.ActiveCount())
	}
}

// --- Credentials before conflict resolution ---

// An unauthenticated force request (bad token) must not revoke the existing
// envoy: the force-revoke runs only after the token has matched a pattern.
func TestRegisterForceBadTokenLeavesVictimActive(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.AddApprovedPattern(ctx, "victim.example.com", "victim-token"); err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	victimID, err := reg.Register(ctx, "victim.example.com", "10.0.0.1", "pubkey-victim", "victim-token", false)
	if err != nil {
		t.Fatalf("victim Register: %v", err)
	}
	// A second unused pattern exists so the attacker's request reaches the
	// bcrypt compare (not the empty-pattern early return).
	if _, err := reg.AddApprovedPattern(ctx, "*.example.com", "real-token"); err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}

	_, err = reg.Register(ctx, "victim.example.com", "10.0.0.66", "pubkey-attacker", "wrong-token", true)
	if !errors.Is(err, registry.ErrNoMatchingPattern) {
		t.Fatalf("expected ErrNoMatchingPattern, got: %v", err)
	}
	if err := reg.ValidateEnvoy(ctx, victimID); err != nil {
		t.Fatalf("victim envoy must stay active after an unauthenticated force request, got: %v", err)
	}
}

// Same for the trusted path: a peer outside the trusted CIDRs must not be
// able to force-revoke.
func TestTrustedRegisterForceOutsideCIDRLeavesVictimActive(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.example.com", CIDRs: []string{"10.0.0.0/8"}},
	}
	victimID, err := reg.TrustedRegister(ctx, "tvictim.example.com", "10.0.0.1", "pubkey-victim", patterns, "10.0.0.50", false)
	if err != nil {
		t.Fatalf("victim TrustedRegister: %v", err)
	}

	_, err = reg.TrustedRegister(ctx, "tvictim.example.com", "10.0.0.1", "pubkey-attacker", patterns, "203.0.113.9", true)
	if !errors.Is(err, registry.ErrNoMatchingPattern) {
		t.Fatalf("expected ErrNoMatchingPattern, got: %v", err)
	}
	if err := reg.ValidateEnvoy(ctx, victimID); err != nil {
		t.Fatalf("victim envoy must stay active after a force request from outside the trusted CIDRs, got: %v", err)
	}
}

// Every enrollment path refuses a hostname that cannot be a cert DNS SAN.
func TestEnrollmentRejectsInvalidHostname(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	patterns := []config.TrustedEnrollment{{Pattern: "*"}}
	if _, err := reg.AddApprovedPattern(ctx, "*", "any-token"); err != nil {
		t.Fatalf("AddApprovedPattern: %v", err)
	}
	for _, h := range []string{"*.example.com", "10.0.0.9", "bad_host", "a..b"} {
		if _, err := reg.Register(ctx, h, "10.0.0.9", "pk", "any-token", false); !errors.Is(err, registry.ErrInvalidHostname) {
			t.Errorf("Register(%q): expected ErrInvalidHostname, got %v", h, err)
		}
		if _, err := reg.TrustedRegister(ctx, h, "10.0.0.9", "pk", patterns, "10.0.0.9", false); !errors.Is(err, registry.ErrInvalidHostname) {
			t.Errorf("TrustedRegister(%q): expected ErrInvalidHostname, got %v", h, err)
		}
		if _, err := reg.CreateEnvoy(ctx, h, "10.0.0.9", "pk", false); !errors.Is(err, registry.ErrInvalidHostname) {
			t.Errorf("CreateEnvoy(%q): expected ErrInvalidHostname, got %v", h, err)
		}
	}
}

// TestRevokePathsUpdateFleetRevokedSet: every registry path that revokes an
// envoy marks it in the fleet index's revoked-id set, which the check-in
// verify path consults instead of the DB — and a fresh enrollment is held in
// the index (not revoked) the moment Register returns, so it can check in.
func TestRevokePathsUpdateFleetRevokedSet(t *testing.T) {
	reg := newTestRegistry(t)
	fi := fleet.NewFleetIndex()
	reg.SetFleetIndex(fi)
	ctx := context.Background()

	if _, err := reg.AddApprovedPattern(ctx, "rs1.example.com", "rs-token-1"); err != nil {
		t.Fatal(err)
	}
	id1, err := reg.Register(ctx, "rs1.example.com", "10.0.9.1", "pubkey-1", "rs-token-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, revoked := fi.GetEnvoyLite(id1); !ok || revoked {
		t.Fatalf("fresh enrollee: in index=%v revoked=%v, want held + not revoked", ok, revoked)
	}
	if fi.IsRevokedID(id1) {
		t.Fatal("fresh enrollee is in the revoked set")
	}

	// Path 1: RevokeEnvoy.
	if err := reg.RevokeEnvoy(ctx, id1); err != nil {
		t.Fatal(err)
	}
	if !fi.IsRevokedID(id1) {
		t.Error("RevokeEnvoy did not mark the id revoked in the fleet index")
	}

	// Path 2: force re-enrollment revokes the active envoy holding the hostname.
	if _, err := reg.AddApprovedPattern(ctx, "rs2.example.com", "rs-token-2"); err != nil {
		t.Fatal(err)
	}
	old, err := reg.Register(ctx, "rs2.example.com", "10.0.9.2", "pubkey-2", "rs-token-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.AddApprovedPattern(ctx, "rs2.example.com", "rs-token-3"); err != nil {
		t.Fatal(err)
	}
	replacement, err := reg.Register(ctx, "rs2.example.com", "10.0.9.2", "pubkey-3", "rs-token-3", true)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsRevokedID(old) {
		t.Error("force re-enrollment did not mark the replaced envoy revoked in the fleet index")
	}
	if fi.IsRevokedID(replacement) {
		t.Error("force re-enrollment marked the replacement envoy revoked")
	}
}
