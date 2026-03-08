package registry_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("hobnob_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		log.Fatalf("failed to start postgres container: %v", err)
	}
	defer func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			log.Fatalf("failed to terminate postgres container: %v", err)
		}
	}()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("failed to get connection string: %v", err)
	}

	// Run migrations
	migrationsPath := findMigrationsDir()
	mig, err := migrate.New("file://"+migrationsPath, "pgx5://"+connStr[len("postgres://"):])
	if err != nil {
		log.Fatalf("failed to create migrate instance: %v", err)
	}
	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("failed to run migrations: %v", err)
	}
	mig.Close()

	testPool, err = pgxpool.New(ctx, connStr)
	if err != nil {
		log.Fatalf("failed to create pool: %v", err)
	}
	defer testPool.Close()

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
// It truncates relevant tables before each test for isolation.
func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	ctx := context.Background()
	_, err := testPool.Exec(ctx, "TRUNCATE approved_patterns, clients CASCADE")
	if err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}
	queries := db.New(testPool)
	return registry.NewRegistry(queries)
}

func TestAddApprovedPattern(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	patternID, err := reg.AddApprovedPattern(ctx, "*.prod.example.com", "my-secret-token")
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}
	if patternID == uuid.Nil {
		t.Fatal("expected non-nil pattern ID")
	}

	// Verify token is bcrypt-hashed (not stored plaintext)
	patterns, err := db.New(testPool).ListUnusedApprovedPatterns(ctx)
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
	if clientID == uuid.Nil {
		t.Fatal("expected non-nil client ID")
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
	if clientID == uuid.Nil {
		t.Fatal("expected non-nil client ID")
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
	if clientID == uuid.Nil {
		t.Fatal("expected non-nil client ID")
	}

	// Verify client was created in DB
	client, err := db.New(testPool).GetClientByID(ctx, clientID)
	if err != nil {
		t.Fatalf("GetClientByID failed: %v", err)
	}
	if client.Hostname != "app01.web.example.com" {
		t.Errorf("hostname = %q, want %q", client.Hostname, "app01.web.example.com")
	}
	if client.IpAddress != "192.168.1.10" {
		t.Errorf("ip_address = %q, want %q", client.IpAddress, "192.168.1.10")
	}
	if client.PubkeyPem != "ed25519-pubkey" {
		t.Errorf("pubkey_pem = %q, want %q", client.PubkeyPem, "ed25519-pubkey")
	}
	if client.Revoked {
		t.Error("newly created client should not be revoked")
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
	if clientID == uuid.Nil {
		t.Fatal("expected non-nil client ID")
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
	patterns, err := db.New(testPool).ListApprovedPatterns(ctx)
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
			if !p.UsedBy.Valid || p.UsedBy.Bytes != clientID {
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

func TestRevokeClient(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a client
	token := "revoke-test-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	clientID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke the client
	err = reg.RevokeClient(ctx, clientID)
	if err != nil {
		t.Fatalf("RevokeClient failed: %v", err)
	}

	// Verify revocation state in DB
	client, err := db.New(testPool).GetClientByID(ctx, clientID)
	if err != nil {
		t.Fatalf("GetClientByID failed: %v", err)
	}
	if !client.Revoked {
		t.Error("expected client to be revoked")
	}
	if !client.RevokedAt.Valid {
		t.Error("expected revoked_at to be set")
	}
}

func TestRevokedClientCheckIn(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a client
	token := "checkin-test-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	clientID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Revoke
	err = reg.RevokeClient(ctx, clientID)
	if err != nil {
		t.Fatalf("RevokeClient failed: %v", err)
	}

	// ValidateClient should return error for revoked client
	err = reg.ValidateClient(ctx, clientID)
	if err == nil {
		t.Fatal("ValidateClient should return error for revoked client")
	}
}

func TestRevokedCannotReenroll(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Create and register a client
	token1 := "first-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token1)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	clientID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-1", token1)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	_ = clientID

	// Revoke
	err = reg.RevokeClient(ctx, clientID)
	if err != nil {
		t.Fatalf("RevokeClient failed: %v", err)
	}

	// Try to re-enroll the same hostname (ENRL-07: revoked clients cannot re-enroll)
	token2 := "second-token"
	_, err = reg.AddApprovedPattern(ctx, "*.example.com", token2)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	_, err = reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey-2", token2)
	if err == nil {
		t.Fatal("Register should fail for hostname of a revoked client (ENRL-07)")
	}
}

func TestValidateClientNonExistent(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	err := reg.ValidateClient(ctx, uuid.New())
	if err == nil {
		t.Fatal("ValidateClient should return error for non-existent client")
	}
}

func TestValidateClientActive(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	token := "active-client-token"
	_, err := reg.AddApprovedPattern(ctx, "*.example.com", token)
	if err != nil {
		t.Fatalf("AddApprovedPattern failed: %v", err)
	}

	clientID, err := reg.Register(ctx, "web01.example.com", "10.0.1.1", "pubkey", token)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Active client should validate successfully
	err = reg.ValidateClient(ctx, clientID)
	if err != nil {
		t.Fatalf("ValidateClient should succeed for active client: %v", err)
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
	if clientID == uuid.Nil {
		t.Fatal("expected non-nil client ID")
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

func TestRevokeNonExistentClient(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	// Revoking a non-existent client should not error (idempotent)
	// or could return an error -- depends on design. Testing behavior.
	err := reg.RevokeClient(ctx, uuid.New())
	// We accept either behavior but log it
	if err != nil {
		fmt.Printf("RevokeClient for non-existent client returned error: %v (acceptable)\n", err)
	}
}
