package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobnob/hobnob/server/ca"
	"github.com/hobnob/hobnob/server/config"
)

func TestBootstrapScript_ReturnsShellScript(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=mytoken123", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("expected Content-Type text/plain, got %s", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	script := string(body)

	if !strings.HasPrefix(script, "#!/bin/sh") && !strings.HasPrefix(script, "#!/usr/bin/env sh") {
		t.Error("bootstrap script should start with a shebang line")
	}
	if !strings.Contains(script, "server.example.com:8080") {
		t.Error("bootstrap script should contain server address")
	}
}

func TestBootstrapScript_ContainsTokenAndServer(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=secret-token-42", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	script := string(body)

	if !strings.Contains(script, "secret-token-42") {
		t.Error("bootstrap script should contain the provided token")
	}
	if !strings.Contains(script, "server.example.com:8080") {
		t.Error("bootstrap script should contain the server address")
	}
}

func TestBootstrapScript_SingleQuotesInterpolatedValues(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=test-token", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	script := string(body)

	// The token and server address should be single-quoted for injection safety.
	if !strings.Contains(script, "'test-token'") {
		t.Error("token should be single-quoted in bootstrap script")
	}
	if !strings.Contains(script, "'https://server.example.com:8080'") {
		t.Error("server address should be single-quoted in bootstrap script")
	}
}

func TestBootstrapScript_WorksWithoutToken(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("FAKE"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	// Token is optional now (trusted enrollment). Script should still be served.
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Result().StatusCode)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.HasPrefix(string(body), "#!/bin/sh") {
		t.Error("bootstrap script should start with shebang even without token")
	}
}

func TestAgentLatest_ReturnsVersionJSON(t *testing.T) {
	dir := t.TempDir()
	// Create a fake agent binary.
	agentDir := filepath.Join(dir, "agents")
	osDir := filepath.Join(agentDir, "linux", "amd64")
	if err := os.MkdirAll(osDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(osDir, "hob"), []byte("binary-content"), 0755); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8080", "", agentDir, "1.2.3")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/latest?os=linux&arch=amd64", nil)
	w := httptest.NewRecorder()
	h.AgentLatest(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Version     string `json:"version"`
		SHA256      string `json:"sha256"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	if body.Version != "1.2.3" {
		t.Errorf("expected version 1.2.3, got %s", body.Version)
	}
	if body.SHA256 == "" {
		t.Error("sha256 should not be empty when binary exists")
	}
	if body.DownloadURL != "/api/v1/agent/download/linux/amd64" {
		t.Errorf("unexpected download_url: %s", body.DownloadURL)
	}
}

func TestAgentDownload_ServesFile(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents")
	osDir := filepath.Join(agentDir, "linux", "amd64")
	if err := os.MkdirAll(osDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("fake-agent-binary-v1")
	if err := os.WriteFile(filepath.Join(osDir, "hob"), content, 0755); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8080", "", agentDir, "1.0.0")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/download/linux/amd64", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fake-agent-binary-v1" {
		t.Errorf("unexpected body: %s", string(body))
	}
}

func TestAgentDownload_Returns404_WhenBinaryMissing(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler("https://server:8080", "", filepath.Join(dir, "agents"), "1.0.0")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/download/linux/amd64", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Result().StatusCode)
	}
}

func TestBootstrapRegister_ReturnsServiceUnavailable_WithoutCA(t *testing.T) {
	h := NewHandler("https://server:8080", "", "", "1.0.0")
	// No CA or registry configured.

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := `{"hostname":"web-01","ip_address":"10.0.1.5","pubkey_pem":"pk","csr_pem":"csr"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Result().StatusCode)
	}
}

func TestBootstrapRegister_ReturnsServiceUnavailable_WithoutRegistry(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandlerWithCA(t, dir)
	// CA is set but no registry.

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := `{"hostname":"web-01","ip_address":"10.0.1.5","pubkey_pem":"pk","csr_pem":"csr"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Result().StatusCode)
	}
}

func TestBootstrapRegister_RejectsMissingFields(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandlerWithCA(t, dir)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Missing hostname.
	body := `{"ip_address":"10.0.1.5","pubkey_pem":"pk","csr_pem":"csr"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestBootstrapRegister_RejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandlerWithCA(t, dir)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/register", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Result().StatusCode)
	}
}

// newTestHandlerWithCA creates a Handler with a test CA for bootstrap register tests.
// It does NOT set a registry (tests that need enrollment must use a real DB).
func newTestHandlerWithCA(t *testing.T, dir string) *Handler {
	t.Helper()

	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(caKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	os.WriteFile(certPath, certPEM, 0644)
	os.WriteFile(keyPath, keyPEM, 0600)

	authority, err := ca.Load(certPath, keyPath)
	if err != nil {
		t.Fatalf("load test CA: %v", err)
	}

	h := NewHandler("https://server:8080", certPath, filepath.Join(dir, "agents"), "1.0.0")
	h.SetCA(authority)
	h.SetTrustedPatterns([]config.TrustedEnrollment{})
	return h
}

// generateTestCSR creates a PEM-encoded CSR for testing.
func generateTestCSR(t *testing.T, hostname string) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: []string{hostname},
	}
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, template, key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

func TestAgentDownload_RejectsInvalidOsArch(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler("https://server:8080", "", filepath.Join(dir, "agents"), "1.0.0")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tests := []struct {
		path string
	}{
		{"/api/v1/agent/download/windows/amd64"},
		{"/api/v1/agent/download/linux/386"},
		{"/api/v1/agent/download/freebsd/arm64"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Result().StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", tt.path, w.Result().StatusCode)
		}
	}
}
