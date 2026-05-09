// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

	"git.alexander4.io/vigo/server/ca"
	"git.alexander4.io/vigo/server/config"
)

func TestBootstrapScript_ReturnsShellScript(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8443", caFile, filepath.Join(dir, "agents"), "1.0.0")

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
	if !strings.Contains(script, "server.example.com:8443") {
		t.Error("bootstrap script should contain server address")
	}
}

func TestBootstrapScript_ContainsTokenAndServer(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8443", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=secret-token-42", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	script := string(body)

	if !strings.Contains(script, "secret-token-42") {
		t.Error("bootstrap script should contain the provided token")
	}
	if !strings.Contains(script, "server.example.com:8443") {
		t.Error("bootstrap script should contain the server address")
	}
}

func TestBootstrapScript_SingleQuotesInterpolatedValues(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8443", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=test-token", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	script := string(body)

	// The token and server address should be single-quoted for injection safety.
	if !strings.Contains(script, "'test-token'") {
		t.Error("token should be single-quoted in bootstrap script")
	}
	if !strings.Contains(script, "'https://server.example.com:8443'") {
		t.Error("server address should be single-quoted in bootstrap script")
	}
}

func TestBootstrapScript_WorksWithoutToken(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("FAKE"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8443", caFile, filepath.Join(dir, "agents"), "1.0.0")

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

// TestBootstrapScript_NoSubstrateNamesAsShellCommands guards against the
// 0.31.0 substrate rename's collateral damage: a blind `echo → longdrawer`
// substitution swept every shell `echo` in the bootstrap template, leaving
// new agent installs failing with `sh: longdrawer: not found`. None of the
// renamed substrate vocabulary is a real shell command, so any line that
// invokes them as commands (`^…$name `) means the substitution slipped
// back in.
func TestBootstrapScript_NoSubstrateNamesAsShellCommands(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("FAKE"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8443", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	script := string(body)

	for _, name := range []string{"longdrawer", "lockbox", "gitmirror", "payload"} {
		// Match the name as a shell command at the start of a line
		// (optionally indented). Doc lines and quoted-arg uses are fine.
		for _, prefix := range []string{name + " ", name + "\t", name + "\n"} {
			for _, lead := range []string{"", "    ", "\t"} {
				needle := lead + prefix
				if strings.Contains(script, "\n"+needle) || strings.HasPrefix(script, needle) {
					t.Errorf("bootstrap script invokes %q as a shell command — substrate-name rename collateral?", name)
				}
			}
		}
	}
}

func TestAgentLatest_ReturnsVersionJSON(t *testing.T) {
	dir := t.TempDir()
	// Create a fake agent binary plus its sidecar version file. The handler
	// reads the version from the sidecar (build pipeline writes it) — this
	// is the same path used in production and is the only way to advertise
	// a version for a binary that can't be executed (cross-arch case).
	agentDir := filepath.Join(dir, "agents")
	osDir := filepath.Join(agentDir, "linux", "amd64")
	if err := os.MkdirAll(osDir, 0755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(osDir, "vigo")
	if err := os.WriteFile(binPath, []byte("binary-content"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath+".version", []byte("1.2.3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8443", "", agentDir, "1.2.3")

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

// TestAgentLatest_PerArchVersions proves that AgentLatest returns the version
// of the binary actually being served (per requested arch), not a single
// server-arch version regardless of the request. This is the regression
// guard for the raspberrypi update loop: amd64 server with amd64=v1 binary
// and arm64=v2 binary must report v1 to amd64 clients and v2 to arm64
// clients — not v1 to both.
func TestAgentLatest_PerArchVersions(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents")

	for _, c := range []struct{ arch, version string }{
		{"amd64", "0.34.1"},
		{"arm64", "0.33.10"}, // the stale one that triggered the loop
	} {
		archDir := filepath.Join(agentDir, "linux", c.arch)
		if err := os.MkdirAll(archDir, 0755); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(archDir, "vigo")
		if err := os.WriteFile(bin, []byte("fake-"+c.arch), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin+".version", []byte(c.version+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHandler("https://server:8443", "", agentDir, "irrelevant-server-version")

	for _, want := range []struct {
		arch    string
		version string
	}{
		{"amd64", "0.34.1"},
		{"x86_64", "0.34.1"}, // alias normalized to amd64
		{"arm64", "0.33.10"},
		{"aarch64", "0.33.10"}, // alias normalized to arm64
	} {
		t.Run(want.arch, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				"/api/v1/agent/latest?os=linux&arch="+want.arch, nil)
			w := httptest.NewRecorder()
			h.AgentLatest(w, req)

			var body struct {
				Version string `json:"version"`
				SHA256  string `json:"sha256"`
			}
			if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Version != want.version {
				t.Errorf("arch=%s: expected version %q, got %q",
					want.arch, want.version, body.Version)
			}
			if body.SHA256 == "" {
				t.Errorf("arch=%s: sha256 should not be empty", want.arch)
			}
		})
	}
}

// TestAgentLatest_UnknownVersionReturnsEmpty confirms that when a binary is
// available but its version is unknown (no sidecar, can't be probed because
// it's a foreign arch), the handler returns Version="" rather than guessing.
// Empty version signals the agent NOT to attempt an update — the safe
// behavior that prevents cross-arch loops.
func TestAgentLatest_UnknownVersionReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents")
	// Put a binary at a path that won't be the test runner's arch+os and
	// has no sidecar. Use freebsd to dodge any risk of the test host's
	// runtime probe succeeding.
	archDir := filepath.Join(agentDir, "freebsd", "arm64")
	if err := os.MkdirAll(archDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archDir, "vigo"), []byte("opaque"), 0755); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8443", "", agentDir, "fallback-version")

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/agent/latest?os=freebsd&arch=arm64", nil)
	w := httptest.NewRecorder()
	h.AgentLatest(w, req)

	var body struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != "" {
		t.Errorf("expected empty version for unknown-version binary, got %q", body.Version)
	}
	if body.SHA256 == "" {
		t.Error("sha256 should still be present (binary exists, just version unknown)")
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
	if err := os.WriteFile(filepath.Join(osDir, "vigo"), content, 0755); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8443", "", agentDir, "1.0.0")

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
	h := NewHandler("https://server:8443", "", filepath.Join(dir, "agents"), "1.0.0")

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
	h := NewHandler("https://server:8443", "", "", "1.0.0")
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

	h := NewHandler("https://server:8443", certPath, filepath.Join(dir, "agents"), "1.0.0")
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
	h := NewHandler("https://server:8443", "", filepath.Join(dir, "agents"), "1.0.0")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tests := []struct {
		path string
	}{
		{"/api/v1/agent/download/linux/386"},
		{"/api/v1/agent/download/solaris/amd64"},
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
