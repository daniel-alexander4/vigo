// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
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
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/registry"
)

var (
	testDB      *sql.DB
	testCertDir string
)

func TestMain(m *testing.M) {
	// Create a temp dir for the SQLite DB file and certs.
	tmpDir, err := os.MkdirTemp("", "vigo-grpc-test-*")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	testDB, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
	if err != nil {
		log.Fatalf("failed to open sqlite: %v", err)
	}
	defer testDB.Close()

	// Run migrations.
	migrationsPath := findMigrationsDir()
	mig, err := migrate.New("file://"+migrationsPath, "sqlite3://"+dbPath)
	if err != nil {
		log.Fatalf("failed to create migrate instance: %v", err)
	}
	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("failed to run migrations: %v", err)
	}
	mig.Close()

	// Generate test TLS certs.
	testCertDir = filepath.Join(tmpDir, "certs")
	if err := os.MkdirAll(testCertDir, 0755); err != nil {
		log.Fatalf("failed to create cert dir: %v", err)
	}
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
	// Clean tables in correct order for FK constraints.
	for _, table := range []string{"run_results", "runs", "envoy_traits", "plans", "plan_results", "file_snapshots", "approved_patterns", "envoys"} {
		if _, err := testDB.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("failed to delete from %s: %v", table, err)
		}
	}

	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Create a minimal config store (empty examples dir with envoys/, modules/, and roles/ subdirs).
	nodeConfigsDir := filepath.Join(t.TempDir(), "examples")
	for _, sub := range []string{"stockpile/modules", "scaffolding/envoys", "scaffolding/roles"} {
		if err := os.MkdirAll(filepath.Join(nodeConfigsDir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}

	configStore, err := config.NewConfigStore(nodeConfigsDir, &noopProvider{}, true)
	if err != nil {
		t.Fatal(err)
	}

	srv, _, _, _, _, err := isgrpc.NewServer(isgrpc.ServerConfig{
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

	client := pb.NewVigoAgentClient(conn)
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
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Add an approved pattern.
	token := "test-registration-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	// Generate an ED25519 keypair for the agent.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	// Register via gRPC.
	client := pb.NewVigoAgentClient(conn)
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
	if resp.GetEnvoyId() == "" {
		t.Fatal("expected non-empty envoy_id")
	}

	// Verify UUID is valid.
	_, err = uuid.Parse(resp.GetEnvoyId())
	if err != nil {
		t.Fatalf("envoy_id is not a valid UUID: %v", err)
	}
}

func TestCheckInRevoked(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Register a client.
	token := "checkin-revoke-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "node1.test.example.com", "10.0.1.1", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	// Revoke the envoy.
	if err := reg.RevokeEnvoy(ctx, envoyID); err != nil {
		t.Fatal(err)
	}

	// CheckIn should fail for revoked envoy.
	agentClient := pb.NewVigoAgentClient(conn)
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("test-fingerprint")
	sig := signPayload(payload, tsStr, priv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:           envoyID,
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
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Register a client.
	token := "checkin-success-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "active1.test.example.com", "10.0.1.2", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	// CheckIn should succeed with valid signature.
	agentClient := pb.NewVigoAgentClient(conn)
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("some-fingerprint")
	sig := signPayload(payload, tsStr, priv)

	resp, err := agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:           envoyID,
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
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Register a client.
	token := "bad-sig-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "badsig.test.example.com", "10.0.1.3", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	// Use a DIFFERENT key to sign (should fail verification).
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	agentClient := pb.NewVigoAgentClient(conn)
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fingerprint")
	sig := signPayload(payload, tsStr, wrongPriv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:           envoyID,
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

// publicKeyPEM exports an ED25519 public key in PKIX PEM format.
func publicKeyPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// signPayload signs payload+timestamp with ED25519 for request authentication.
func signPayload(payload []byte, timestamp string, key ed25519.PrivateKey) []byte {
	msg := make([]byte, len(payload)+len(timestamp))
	copy(msg, payload)
	copy(msg[len(payload):], timestamp)
	return ed25519.Sign(key, msg)
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

func TestCheckInStaleTimestamp(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	token := "stale-ts-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "stale1.test.example.com", "10.0.1.10", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	// Timestamp 6 minutes in the past — outside the 5-minute window.
	now := time.Now().Add(-6 * time.Minute)
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fp")
	sig := signPayload(payload, tsStr, priv)

	agentClient := pb.NewVigoAgentClient(conn)
	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with stale timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

func TestCheckInFutureTimestamp(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	token := "future-ts-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "future1.test.example.com", "10.0.1.11", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	// Timestamp 6 minutes in the future — outside the 5-minute window.
	now := time.Now().Add(6 * time.Minute)
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fp")
	sig := signPayload(payload, tsStr, priv)

	agentClient := pb.NewVigoAgentClient(conn)
	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with future timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

func TestCheckInTimestampBoundary(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	token := "boundary-ts-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "boundary1.test.example.com", "10.0.1.12", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	agentClient := pb.NewVigoAgentClient(conn)

	// 4min59s ago — should succeed (within the 5-minute window).
	withinWindow := time.Now().Add(-4*time.Minute - 59*time.Second)
	tsStr := fmt.Sprintf("%d", withinWindow.Unix())
	payload := []byte("fp-ok")
	sig := signPayload(payload, tsStr, priv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp-ok",
		Signature:        sig,
		Timestamp:        timestamppb.New(withinWindow),
	})
	if err != nil {
		t.Fatalf("expected CheckIn to succeed with 4m59s-old timestamp, got: %v", err)
	}

	// 5min1s ago — should fail (outside the 5-minute window).
	outsideWindow := time.Now().Add(-5*time.Minute - 1*time.Second)
	tsStr = fmt.Sprintf("%d", outsideWindow.Unix())
	payload = []byte("fp-fail")
	sig = signPayload(payload, tsStr, priv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp-fail",
		Signature:        sig,
		Timestamp:        timestamppb.New(outsideWindow),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with 5m1s-old timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

func TestCheckInTamperedSignature(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	token := "tampered-sig-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "tampered1.test.example.com", "10.0.1.13", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fp")
	sig := signPayload(payload, tsStr, priv)
	sig[len(sig)-1] ^= 0xff // flip last byte

	agentClient := pb.NewVigoAgentClient(conn)
	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with tampered signature")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

func TestCheckInTruncatedSignature(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	token := "truncated-sig-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "truncated1.test.example.com", "10.0.1.14", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fp")
	sig := signPayload(payload, tsStr, priv)
	sig = sig[:32] // truncate to 32 bytes (ED25519 signatures are 64 bytes)

	agentClient := pb.NewVigoAgentClient(conn)
	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with truncated signature")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

func TestCheckInMissingTimestamp(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	token := "missing-ts-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "missingts1.test.example.com", "10.0.1.15", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	// Create a valid signature with the current time, but omit the timestamp from the request.
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fp")
	sig := signPayload(payload, tsStr, priv)
	_ = priv // used above

	agentClient := pb.NewVigoAgentClient(conn)
	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp",
		Signature:        sig,
		Timestamp:        nil, // deliberately missing
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with missing timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

func TestCheckInMalformedUUID(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	now := time.Now()

	agentClient := pb.NewVigoAgentClient(conn)
	_, err := agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         "not-a-uuid",
		StateFingerprint: "fp",
		Signature:        []byte("fake-sig"),
		Timestamp:        timestamppb.New(now),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail with malformed UUID")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

func TestRevokePreventReregistration(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Register a envoy.
	token1 := "rereg-block-token-1"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token1)
	if err != nil {
		t.Fatal(err)
	}

	pub1, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM1, err := publicKeyPEM(pub1)
	if err != nil {
		t.Fatal(err)
	}

	agentClient := pb.NewVigoAgentClient(conn)
	resp, err := agentClient.Register(ctx, &pb.RegisterRequest{
		Hostname:  "reregfail.test.example.com",
		IpAddress: "10.0.1.16",
		PubkeyPem: pubPEM1,
		Token:     token1,
	})
	if err != nil {
		t.Fatalf("initial Register failed: %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("initial Register returned success=false: %s", resp.GetMessage())
	}
	envoyID := resp.GetEnvoyId()

	// Revoke the envoy.
	if err := reg.RevokeEnvoy(ctx, envoyID); err != nil {
		t.Fatalf("RevokeEnvoy failed: %v", err)
	}

	// Generate a new keypair and add a new approved pattern for re-registration.
	token2 := "rereg-block-token-2"
	_, err = reg.AddApprovedPattern(ctx, "*.test.example.com", token2)
	if err != nil {
		t.Fatal(err)
	}

	pub2, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM2, err := publicKeyPEM(pub2)
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to re-register the same hostname — should be blocked.
	_, err = agentClient.Register(ctx, &pb.RegisterRequest{
		Hostname:  "reregfail.test.example.com",
		IpAddress: "10.0.1.16",
		PubkeyPem: pubPEM2,
		Token:     token2,
	})
	if err == nil {
		t.Fatal("expected re-registration of revoked hostname to fail")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v: %s", st.Code(), st.Message())
	}
}

func TestRevokeAfterCheckIn(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	conn := dialTLS(t, addr)
	defer conn.Close()

	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	token := "revoke-after-checkin-token"
	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, "revokeafter1.test.example.com", "10.0.1.17", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	agentClient := pb.NewVigoAgentClient(conn)

	// First CheckIn should succeed.
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte("fp-before-revoke")
	sig := signPayload(payload, tsStr, priv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp-before-revoke",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	})
	if err != nil {
		t.Fatalf("first CheckIn (before revoke) failed: %v", err)
	}

	// Revoke the envoy.
	if err := reg.RevokeEnvoy(ctx, envoyID); err != nil {
		t.Fatalf("RevokeEnvoy failed: %v", err)
	}

	// Second CheckIn should fail with PermissionDenied.
	now2 := time.Now()
	tsStr2 := fmt.Sprintf("%d", now2.Unix())
	payload2 := []byte("fp-after-revoke")
	sig2 := signPayload(payload2, tsStr2, priv)

	_, err = agentClient.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp-after-revoke",
		Signature:        sig2,
		Timestamp:        timestamppb.New(now2),
	})
	if err == nil {
		t.Fatal("expected CheckIn to fail after revocation")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v: %s", st.Code(), st.Message())
	}
}
