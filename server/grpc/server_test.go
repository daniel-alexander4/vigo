package grpc_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/isogov/isogov/agent/auth"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/config"
	"github.com/isogov/isogov/server/db"
	isgrpc "github.com/isogov/isogov/server/grpc"
	"github.com/isogov/isogov/server/registry"
)

var (
	testPool    *pgxpool.Pool
	testCertDir string
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("isogov_test"),
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

	// Run migrations.
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

	// Generate test TLS certs.
	testCertDir, err = os.MkdirTemp("", "isogov-test-certs-*")
	if err != nil {
		log.Fatalf("failed to create temp cert dir: %v", err)
	}
	defer os.RemoveAll(testCertDir)

	if err := generateTestCerts(testCertDir); err != nil {
		log.Fatalf("failed to generate test certs: %v", err)
	}

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

// startTestServer creates a gRPC server with mTLS and returns its address and cleanup func.
func startTestServer(t *testing.T) (string, func()) {
	t.Helper()
	ctx := context.Background()
	_, err := testPool.Exec(ctx, "TRUNCATE approved_patterns, clients CASCADE")
	if err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	queries := db.New(testPool)
	reg := registry.NewRegistry(queries)

	// Create a minimal config store (empty clients dir).
	tmpDir := t.TempDir()
	clientsDir := filepath.Join(tmpDir, "clients")
	if err := os.MkdirAll(clientsDir, 0755); err != nil {
		t.Fatal(err)
	}
	classFile := filepath.Join(tmpDir, "classification.yaml")

	configStore, err := config.NewConfigStore(clientsDir, classFile, &noopProvider{})
	if err != nil {
		t.Fatal(err)
	}

	srv, _, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:    filepath.Join(testCertDir, "server.crt"),
		KeyFile:     filepath.Join(testCertDir, "server.key"),
		CAFile:      filepath.Join(testCertDir, "ca.crt"),
		GRPCPort:    ":0",
		Registry:    reg,
		ConfigStore: configStore,
		Queries:     queries,
	})
	if err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go srv.Serve(lis)

	return lis.Addr().String(), func() {
		srv.Stop()
	}
}

// dialTLS creates a gRPC client connection with mTLS using the test certs.
func dialTLS(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()

	cert, err := tls.LoadX509KeyPair(
		filepath.Join(testCertDir, "client.crt"),
		filepath.Join(testCertDir, "client.key"),
	)
	if err != nil {
		t.Fatal(err)
	}

	caCert, err := os.ReadFile(filepath.Join(testCertDir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCert)

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestMTLSRequired(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	// Try connecting without TLS -- should fail.
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("unexpected dial error: %v", err)
	}
	defer conn.Close()

	client := pb.NewIsgovAgentClient(conn)
	_, err = client.Register(context.Background(), &pb.RegisterRequest{
		Hostname:  "test-host",
		IpAddress: "10.0.0.1",
		PubkeyPem: "test-pubkey",
		Token:     "test-token",
	})
	if err == nil {
		t.Fatal("expected plaintext connection to be rejected")
	}
	// The connection should fail with a transport error.
	st, ok := status.FromError(err)
	if ok && st.Code() == codes.OK {
		t.Fatal("expected error code, got OK")
	}
}

func TestRegisterFlow(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testPool)
	reg := registry.NewRegistry(queries)

	// Add an approved pattern.
	token := "test-registration-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	// Generate an ED25519 keypair for the agent.
	pub, _, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := auth.PublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	// Register via gRPC.
	client := pb.NewIsgovAgentClient(conn)
	resp, err := client.Register(ctx, &pb.RegisterRequest{
		Hostname:  "web01.test.example.com",
		IpAddress: "10.0.1.5",
		PubkeyPem: pubPEM,
		Token:     token,
	})
	if err != nil {
		t.Fatalf("Register RPC failed: %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("Register returned success=false: %s", resp.GetMessage())
	}
	if resp.GetClientId() == "" {
		t.Fatal("expected non-empty client_id")
	}

	// Verify UUID is valid.
	_, err = uuid.Parse(resp.GetClientId())
	if err != nil {
		t.Fatalf("client_id is not a valid UUID: %v", err)
	}
}

func TestCheckInRevoked(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testPool)
	reg := registry.NewRegistry(queries)

	// Register a client.
	token := "checkin-revoke-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := auth.PublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	clientID, err := reg.Register(ctx, "node1.test.example.com", "10.0.1.1", pubPEM, token)
	if err != nil {
		t.Fatal(err)
	}

	// Revoke the client.
	if err := reg.RevokeClient(ctx, clientID); err != nil {
		t.Fatal(err)
	}

	// CheckIn should fail for revoked client.
	agentClient := pb.NewIsgovAgentClient(conn)
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("test-fingerprint")
	sig := auth.SignPayload(payload, tsStr, priv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		ClientId:         clientID.String(),
		StateFingerprint: "test-fingerprint",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail for revoked client")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v: %s", st.Code(), st.Message())
	}
}

func TestCheckInSuccess(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testPool)
	reg := registry.NewRegistry(queries)

	// Register a client.
	token := "checkin-success-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := auth.PublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	clientID, err := reg.Register(ctx, "active1.test.example.com", "10.0.1.2", pubPEM, token)
	if err != nil {
		t.Fatal(err)
	}

	// CheckIn should succeed with valid signature.
	agentClient := pb.NewIsgovAgentClient(conn)
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("some-fingerprint")
	sig := auth.SignPayload(payload, tsStr, priv)

	resp, err := agentClient.CheckIn(ctx, &pb.CheckInRequest{
		ClientId:         clientID.String(),
		StateFingerprint: "some-fingerprint",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err != nil {
		t.Fatalf("CheckIn failed: %v", err)
	}
	// No config set up, so modules should be empty.
	if len(resp.GetModules()) != 0 {
		t.Errorf("expected 0 modules for unconfigured client, got %d", len(resp.GetModules()))
	}
}

func TestCheckInBadSignature(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testPool)
	reg := registry.NewRegistry(queries)

	// Register a client.
	token := "bad-sig-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, _, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := auth.PublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	clientID, err := reg.Register(ctx, "badsig.test.example.com", "10.0.1.3", pubPEM, token)
	if err != nil {
		t.Fatal(err)
	}

	// Use a DIFFERENT key to sign (should fail verification).
	_, wrongPriv, err := auth.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}

	agentClient := pb.NewIsgovAgentClient(conn)
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fingerprint")
	sig := auth.SignPayload(payload, tsStr, wrongPriv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		ClientId:         clientID.String(),
		StateFingerprint: "fingerprint",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with wrong signature")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", st.Code())
	}
}

// noopProvider is a minimal secrets.Provider for tests.
type noopProvider struct{}

func (p *noopProvider) Get(_ context.Context, key string) (string, error) {
	return "", fmt.Errorf("no secret for %q", key)
}
func (p *noopProvider) GetBytes(_ context.Context, key string) ([]byte, error) {
	return nil, fmt.Errorf("no secret for %q", key)
}
func (p *noopProvider) Close() error { return nil }

// generateTestCerts creates a CA, server cert, and client cert for testing.
func generateTestCerts(dir string) error {
	// Generate CA key.
	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPriv.Public(), caPriv)
	if err != nil {
		return err
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return err
	}

	if err := writePEM(filepath.Join(dir, "ca.crt"), "CERTIFICATE", caCertDER); err != nil {
		return err
	}

	// Generate server cert.
	_, srvPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	srvTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	srvCertDER, err := x509.CreateCertificate(rand.Reader, srvTemplate, caCert, srvPriv.Public(), caPriv)
	if err != nil {
		return err
	}

	if err := writePEM(filepath.Join(dir, "server.crt"), "CERTIFICATE", srvCertDER); err != nil {
		return err
	}
	if err := writePrivKeyPEM(filepath.Join(dir, "server.key"), srvPriv); err != nil {
		return err
	}

	// Generate client cert (for mTLS).
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, clientPriv.Public(), caPriv)
	if err != nil {
		return err
	}

	if err := writePEM(filepath.Join(dir, "client.crt"), "CERTIFICATE", clientCertDER); err != nil {
		return err
	}
	if err := writePrivKeyPEM(filepath.Join(dir, "client.key"), clientPriv); err != nil {
		return err
	}

	return nil
}

func writePEM(path, blockType string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type:  blockType,
		Bytes: der,
	}), 0644)
}

func writePrivKeyPEM(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	}), 0600)
}
