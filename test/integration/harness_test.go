// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package integration provides cross-binary tests that spawn the Go server
// and the Rust agent binary together, exercising the full enrollment →
// check-in → convergence → report-result path over real mTLS gRPC.
//
// Prerequisites:
//   - Agent binary at bin/vigo (run `make build-agent` first)
//   - Migrations in migrations/ (found automatically)
package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/registry"
)

// ---------- package-level fixtures ----------

var (
	testDB      *sql.DB
	testCertDir string
	agentBin    string
	repoRoot    string
)

func TestMain(m *testing.M) {
	root := findRepoRoot()
	repoRoot = root

	// Verify agent binary exists.
	agentBin = filepath.Join(root, "bin", "vigo")
	if _, err := os.Stat(agentBin); err != nil {
		log.Fatalf("agent binary not found at %s — run 'make build-agent' first", agentBin)
	}

	// Create temp dir for DB + certs.
	tmpDir, err := os.MkdirTemp("", "vigo-integration-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Open SQLite.
	dbPath := filepath.Join(tmpDir, "test.db")
	testDB, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
	if err != nil {
		log.Fatal(err)
	}
	defer testDB.Close()

	// Run migrations.
	migsDir := filepath.Join(root, "migrations")
	mig, err := migrate.New("file://"+migsDir, "sqlite3://"+dbPath)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("migrate up: %v", err)
	}
	mig.Close()

	// Generate TLS certs.
	testCertDir = filepath.Join(tmpDir, "certs")
	os.MkdirAll(testCertDir, 0755)
	if err := generateTestCerts(testCertDir); err != nil {
		log.Fatalf("generate certs: %v", err)
	}

	os.Exit(m.Run())
}

// ---------- server lifecycle ----------

// testEnv bundles everything a single test needs.
type testEnv struct {
	ServerAddr string            // gRPC address (127.0.0.1:<port>)
	Queries    *db.Queries
	Registry   *registry.Registry
	ConfigDir  string
	Cleanup    func()
}

// startServer creates a gRPC server with the given modules and envoy mappings.
// It cleans tables, creates a ConfigStore, and listens on a random port.
func startServer(t *testing.T, modules map[string]string, envoysYAML string) *testEnv {
	t.Helper()
	ctx := context.Background()

	// Clean tables.
	for _, table := range []string{
		"task_run_targets", "task_runs", "workflow_step_results", "workflow_runs",
		"run_results", "runs", "envoy_traits", "plans", "plan_results",
		"file_snapshots", "approved_patterns", "envoys",
	} {
		if _, err := testDB.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}

	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	// Build the stacks directory (0.33.0 layout): stockpile/ holds every
	// config primitive (modules, matchfiles, etc.). Config extension is
	// .vgo; .yaml is not recognized.
	configDir := filepath.Join(t.TempDir(), "stacks")
	modulesDir := filepath.Join(configDir, "stockpile", "modules")
	stockpileDir := filepath.Join(configDir, "stockpile")
	os.MkdirAll(modulesDir, 0755)
	os.MkdirAll(stockpileDir, 0755)
	for name, content := range modules {
		os.WriteFile(filepath.Join(modulesDir, name+".vgo"), []byte(content), 0644)
	}
	if envoysYAML != "" {
		os.WriteFile(filepath.Join(stockpileDir, "envoys.vgo"), []byte(envoysYAML), 0644)
	}

	configStore, err := config.NewConfigStore(configDir, &noopProvider{}, true)
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

	return &testEnv{
		ServerAddr: lis.Addr().String(),
		Queries:    queries,
		Registry:   reg,
		ConfigDir:  configDir,
		Cleanup:    func() { srv.Stop() },
	}
}

// ---------- agent data directory ----------

// prepareAgentDir creates a data directory for the agent with:
//   - ED25519 keypair (agent.key)
//   - mTLS client certs (certs/{ca.crt, tls.crt, tls.key})
//
// It returns the data dir path and the ED25519 public key PEM for registration.
func prepareAgentDir(t *testing.T) (dataDir string, pubPEM string) {
	t.Helper()
	dataDir = filepath.Join(t.TempDir(), "agent-data")
	os.MkdirAll(filepath.Join(dataDir, "certs"), 0755)

	// Copy client TLS certs so the agent can do mTLS.
	for _, pair := range [][2]string{
		{"ca.crt", "certs/ca.crt"},
		{"client.crt", "certs/tls.crt"},
		{"client.key", "certs/tls.key"},
	} {
		src := filepath.Join(testCertDir, pair[0])
		dst := filepath.Join(dataDir, pair[1])
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(dst, data, 0600)
	}

	// Generate ED25519 keypair for request signing.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Write private key as PKCS8 PEM (same format the agent uses).
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	os.WriteFile(filepath.Join(dataDir, "agent.key"), privPEM, 0600)

	// Export public key as PKIX PEM for registration.
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEMStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	return dataDir, pubPEMStr
}

// enrollAgent registers the agent server-side and writes envoy.uuid to the agent data dir.
func enrollAgent(t *testing.T, env *testEnv, dataDir, pubPEM, hostname, token string) string {
	t.Helper()
	ctx := context.Background()

	_, err := env.Registry.AddApprovedPattern(ctx, "*.integ.test", token)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := env.Registry.Register(ctx, hostname, "127.0.0.1", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(dataDir, "envoy.uuid"), []byte(envoyID+"\n"), 0600)
	return envoyID
}

// ---------- agent binary runner ----------

// runAgent executes `bin/vigo run` (single convergence cycle) and returns
// combined stdout+stderr output and any error.
func runAgent(t *testing.T, serverAddr, dataDir string, extraArgs ...string) (string, error) {
	t.Helper()

	args := []string{
		"--server", serverAddr,
		"--log-level", "debug",
		"--snapshot-dir", filepath.Join(dataDir, "snapshots"),
		"--executors-dir", filepath.Join(dataDir, "executors"),
		"run",
	}
	args = append(args, extraArgs...)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agentBin, args...)
	// Relocate every agent path into the test's tempdir. The --data-dir
	// CLI flag is gone; the agent now reads these overrides from the
	// environment. See agent/src/platform/paths.rs for the full list.
	cmd.Env = append(os.Environ(),
		"VIGO_DATA_DIR="+dataDir,
		"VIGO_STATE_DIR="+filepath.Join(dataDir, "state"),
		"VIGO_SNAPSHOT_DIR="+filepath.Join(dataDir, "snapshots"),
		"VIGO_FLAGS_DIR="+filepath.Join(dataDir, "flags"),
		"VIGO_BLOBS_DIR="+filepath.Join(dataDir, "swarm", ".blobs"),
		"VIGO_SEEDS_DIR="+filepath.Join(dataDir, "swarm", "seed"),
		"VIGO_GRAVEYARD_DIR="+filepath.Join(dataDir, "swarm", "graveyard"),
		"VIGO_ECHO_DIR="+filepath.Join(dataDir, "echo"),
		"VIGO_EXECUTORS_DIR="+filepath.Join(dataDir, "executors"),
	)

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ---------- TLS cert generation ----------

func generateTestCerts(dir string) error {
	// CA key + cert.
	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Integration Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPriv.Public(), caPriv)
	if err != nil {
		return err
	}
	caCert, _ := x509.ParseCertificate(caCertDER)
	writePEM(filepath.Join(dir, "ca.crt"), "CERTIFICATE", caCertDER)

	// Server cert.
	_, srvPriv, _ := ed25519.GenerateKey(rand.Reader)
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
	srvCertDER, _ := x509.CreateCertificate(rand.Reader, srvTemplate, caCert, srvPriv.Public(), caPriv)
	writePEM(filepath.Join(dir, "server.crt"), "CERTIFICATE", srvCertDER)
	writePrivKeyPEM(filepath.Join(dir, "server.key"), srvPriv)

	// Client cert (for agent mTLS).
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "integration-test-agent"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientCertDER, _ := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, clientPriv.Public(), caPriv)
	writePEM(filepath.Join(dir, "client.crt"), "CERTIFICATE", clientCertDER)
	writePrivKeyPEM(filepath.Join(dir, "client.key"), clientPriv)

	return nil
}

func writePEM(path, blockType string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0644)
}

func writePrivKeyPEM(path string, key ed25519.PrivateKey) error {
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600)
}

// ---------- helpers ----------

// noopProvider rejects all secret lookups — config uses no secret: refs in tests.
type noopProvider struct{}

func (p *noopProvider) Get(_ context.Context, key string) (string, error) {
	return "", fmt.Errorf("noopProvider: %q not found", key)
}
func (p *noopProvider) GetBytes(_ context.Context, key string) ([]byte, error) {
	return nil, fmt.Errorf("noopProvider: %q not found", key)
}
func (p *noopProvider) Close() error { return nil }

func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			log.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

// agentOutput checks combined output for expected strings.
func assertOutputContains(t *testing.T, output, substr string) {
	t.Helper()
	if !strings.Contains(output, substr) {
		t.Errorf("output missing %q\n--- output ---\n%s", substr, truncate(output, 2000))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
