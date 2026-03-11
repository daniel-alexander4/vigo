package registry_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/registry"
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
	tmpDir, err := os.MkdirTemp("", "hobnob-registry-test-*")
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
	for _, table := range []string{"run_results", "runs", "thrall_traits", "plans", "plan_results", "file_snapshots", "approved_patterns", "thralls"} {
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
	clientID, err := reg.Register(ctx, "web01.prod.example.com", "10.0.1.5", "ssh-ed25519-pubkey-pem", token)
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
	_, err = reg.Register(ctx, "web01.prod.example.com", "10.0.2.5", "ssh-ed25519-pubkey-pem-2", "another-token")
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
	clientID, err := reg.Register(ctx, "some-host", "10.0.1.5", "ssh-ed25519-pubkey-pem", token)
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

	clientID, err := reg.Register(ctx, "app01.web.example.com", "192.168.1.10", "ed25519-pubkey", token)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if clientID == "" {
		t.Fatal("expected non-empty client ID")
	}

	// Verify thrall was created in DB
	thrall, err := db.New(testDB).GetThrallByID(ctx, clientID)
	if err != nil {
		t.Fatalf("GetThrallByID failed: %v", err)
	}
	if thrall.Hostname != "app01.web.example.com" {
		t.Errorf("hostname = %q, want %q", thrall.Hostname, "app01.web.example.com")
	}
	if thrall.IpAddress != "192.168.1.10" {
		t.Errorf("ip_address = %q, want %q", thrall.IpAddress, "192.168.1.10")
	}
	if thrall.PubkeyPem != "ed25519-pubkey" {
		t.Errorf("pubkey_pem = %q, want %q", thrall.PubkeyPem, "ed25519-pubkey")
	}
	if thrall.Revoked != 0 {
		t.Error("newly created thrall should not be revoked")
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
	clientID, err := reg.Register(ctx, "node1.secure.example.com", "10.0.0.1", "pubkey1", token)
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
	_, err = reg.Register(ctx, "node2.secure.example.com", "10.0.0.2", "pubkey2", "wrong-token")
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

	clientID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-web01", token)
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
	_, err = reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-1", token)
	if err != nil {
		t.Fatalf("First Register failed: %v", err)
	}

	// Second registration with same token should fail (one-time use)
	_, err = reg.Register(ctx, "web02.example.com", "10.0.1.2", "pubkey-2", token)
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
	_, err = reg.Register(ctx, "rogue-host.evil.com", "192.168.0.1", "bad-pubkey", "token123")
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
	_, err = reg.Register(ctx, "app01.web.example.com", "10.0.1.1", "pubkey", "wrong-token")
	if err == nil {
		t.Fatal("Register should fail with wrong token even if pattern matches")
	}
}

func TestRevokeThrall(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a thrall
	token := "revoke-test-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	thrallID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke the thrall
	err = reg.RevokeThrall(ctx, thrallID)
	if err != nil {
		t.Fatalf("RevokeThrall failed: %v", err)
	}

	// Verify revocation state in DB
	thrall, err := db.New(testDB).GetThrallByID(ctx, thrallID)
	if err != nil {
		t.Fatalf("GetThrallByID failed: %v", err)
	}
	if thrall.Revoked == 0 {
		t.Error("expected thrall to be revoked")
	}
	if !thrall.RevokedAt.Valid {
		t.Error("expected revoked_at to be set")
	}
}

func TestRevokedNodeCheckIn(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a thrall
	token := "checkin-test-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	thrallID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke
	err = reg.RevokeThrall(ctx, thrallID)
	if err != nil {
		t.Fatalf("RevokeThrall failed: %v", err)
	}

	// ValidateNode should return error for revoked thrall
	err = reg.ValidateThrall(ctx, thrallID)
	if err == nil {
		t.Fatal("ValidateNode should return error for revoked thrall")
	}
}

func TestRevokedCannotReenroll(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a thrall
	token1 := "first-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token1)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	thrallID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-1", token1)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke
	err = reg.RevokeThrall(ctx, thrallID)
	if err != nil {
		t.Fatalf("RevokeThrall failed: %v", err)
	}

	// Try to re-enroll the same hostname (ENRL-07: revoked thralls cannot re-enroll)
	token2 := "second-token"
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", token2)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	_, err = reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-2", token2)
	if err == nil {
		t.Fatal("Register should fail for hostname of a revoked thrall (ENRL-07)")
	}
}

func TestValidateNodeNonExistent(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	err := reg.ValidateThrall(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("ValidateNode should return error for non-existent thrall")
	}
}

func TestValidateNodeActive(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "active-thrall-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	thrallID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Active thrall should validate successfully
	err = reg.ValidateThrall(ctx, thrallID)
	if err != nil {
		t.Fatalf("ValidateNode should succeed for active thrall: %v", err)
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
	clientID, err := reg.Register(ctx, "web01.prod.example.com", "192.168.1.1", "pubkey", "prod-token")
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
	_, err = reg.Register(ctx, "some-host", "10.0.2.5", "pubkey", token)
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
	_, err := reg.Register(ctx, "", "10.0.1.1", "pubkey", "token")
	if err == nil {
		t.Fatal("Register should fail with empty hostname")
	}

	// Empty token
	_, err = reg.Register(ctx, "host.example.com", "10.0.1.1", "pubkey", "")
	if err == nil {
		t.Fatal("Register should fail with empty token")
	}

	// Empty pubkey
	_, err = reg.Register(ctx, "host.example.com", "10.0.1.1", "", "token")
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

	thrallID, err := reg.TrustedRegister(ctx, "app01.web.example.com", "10.0.1.5", "ed25519-pubkey", patterns, "10.0.1.100")
	if err != nil {
		t.Fatalf("TrustedRegister should succeed: %v", err)
	}
	if thrallID == "" {
		t.Fatal("expected non-empty thrall ID")
	}
}

func TestTrustedRegisterCIDRReject(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.web.example.com", CIDRs: []string{"10.0.1.0/24"}},
	}

	// Peer IP outside allowed CIDR.
	_, err := reg.TrustedRegister(ctx, "app01.web.example.com", "10.0.1.5", "ed25519-pubkey", patterns, "192.168.1.100")
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

	thrallID, err := reg.TrustedRegister(ctx, "app01.open.example.com", "anywhere", "ed25519-pubkey", patterns, "203.0.113.50")
	if err != nil {
		t.Fatalf("TrustedRegister should succeed with no CIDR restriction: %v", err)
	}
	if thrallID == "" {
		t.Fatal("expected non-empty thrall ID")
	}
}

func TestTrustedRegisterNoMatch(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patterns := []config.TrustedEnrollment{
		{Pattern: "*.web.example.com", CIDRs: []string{"10.0.0.0/8"}},
	}

	_, err := reg.TrustedRegister(ctx, "rogue-host.evil.com", "10.0.1.5", "ed25519-pubkey", patterns, "10.0.1.5")
	if err == nil {
		t.Fatal("TrustedRegister should reject when hostname doesn't match pattern")
	}
}

func TestTrustedRegisterRevokedHostname(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and revoke a thrall with token flow.
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", "tok")
	if err != nil {
		t.Fatal(err)
	}
	thrallID, err := reg.Register(ctx, "revoked.example.com", "10.0.1.1", "pk1", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RevokeThrall(ctx, thrallID); err != nil {
		t.Fatal(err)
	}

	// Try to re-enroll the revoked hostname via trusted enrollment.
	patterns := []config.TrustedEnrollment{
		{Pattern: "*.example.com"},
	}
	_, err = reg.TrustedRegister(ctx, "revoked.example.com", "10.0.1.1", "pk2", patterns, "10.0.1.1")
	if err == nil {
		t.Fatal("TrustedRegister should reject revoked hostname")
	}
}

func TestRevokeNonExistentNode(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Revoking a non-existent thrall should not error (idempotent)
	// or could return an error -- depends on design. Testing behavior.
	err := reg.RevokeThrall(ctx, "00000000-0000-0000-0000-000000000000")
	// We accept either behavior but log it
	if err != nil {
		fmt.Printf("RevokeThrall for non-existent thrall returned error: %v (acceptable)\n", err)
	}
}
