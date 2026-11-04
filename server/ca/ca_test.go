// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// generateTestCA creates a self-signed CA cert and key in the given directory.
func generateTestCA(t *testing.T, dir string) (string, string) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	certPath := filepath.Join(dir, "ca.crt")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatalf("write CA cert: %v", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		t.Fatalf("marshal CA key: %v", err)
	}

	keyPath := filepath.Join(dir, "ca.key")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write CA key: %v", err)
	}

	return certPath, keyPath
}

// generateTestCSR creates a CSR signed by a fresh ECDSA key.
func generateTestCSR(t *testing.T, hostname string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: []string{hostname},
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateTestCA(t, dir)

	ca, err := Load(certPath, keyPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if ca.cert == nil {
		t.Fatal("CA cert should not be nil")
	}
	if len(ca.CACertPEM()) == 0 {
		t.Fatal("CACertPEM should not be empty")
	}
}

func TestLoadMissingCert(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"))
	if err == nil {
		t.Fatal("Load should fail with missing files")
	}
}

func TestSignCSR(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateTestCA(t, dir)

	authority, err := Load(certPath, keyPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	csrPEM := generateTestCSR(t, "web-01.example.com")

	signedPEM, err := authority.SignCSR(csrPEM, "web-01.example.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("SignCSR failed: %v", err)
	}

	// Parse the signed certificate.
	block, _ := pem.Decode(signedPEM)
	if block == nil {
		t.Fatal("signed cert PEM should decode")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse signed cert: %v", err)
	}

	if cert.Subject.CommonName != "web-01.example.com" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "web-01.example.com")
	}

	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "web-01.example.com" {
		t.Errorf("DNSNames = %v, want [web-01.example.com]", cert.DNSNames)
	}

	if len(cert.ExtKeyUsage) != 2 {
		t.Fatalf("signed cert should have 2 EKUs, got %d", len(cert.ExtKeyUsage))
	}
	hasClient, hasServer := false, false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
		if eku == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
	}
	if !hasClient || !hasServer {
		t.Errorf("signed cert should have both ClientAuth and ServerAuth EKU, got %v", cert.ExtKeyUsage)
	}

	// Verify the cert is signed by the CA (check both usages).
	pool := x509.NewCertPool()
	pool.AddCert(authority.cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("cert verification (ClientAuth) failed: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("cert verification (ServerAuth) failed: %v", err)
	}
}

func TestSignCSR_InvalidPEM(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateTestCA(t, dir)

	authority, err := Load(certPath, keyPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	_, err = authority.SignCSR([]byte("not a PEM"), "host", 24*time.Hour)
	if err == nil {
		t.Fatal("SignCSR should fail with invalid PEM")
	}
}

func TestValidateHostname(t *testing.T) {
	valid := []string{"web01", "web-01.example.com", "A1.B2.c3", "x"}
	for _, h := range valid {
		if err := ValidateHostname(h); err != nil {
			t.Errorf("ValidateHostname(%q) = %v, want nil", h, err)
		}
	}
	invalid := []string{
		"", "*", "*.example.com", "web.*.com", "10.0.0.1", "::1", "fe80::1",
		"bad_host", "-web.example.com", "web-.example.com", "a..b", "trailing.",
		".leading", "sp ace", "unié.com", strings.Repeat("a", 64) + ".com",
		strings.Repeat("a.", 127) + "aaa",
	}
	for _, h := range invalid {
		if err := ValidateHostname(h); err == nil {
			t.Errorf("ValidateHostname(%q) = nil, want error", h)
		}
	}
}

// SignCSR itself refuses a wildcard / IP-literal name even if a caller
// skipped the enrollment-time validation.
func TestSignCSR_RejectsInvalidHostname(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateTestCA(t, dir)
	authority, err := Load(certPath, keyPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	for _, h := range []string{"*.example.com", "10.0.0.1"} {
		if _, err := authority.SignCSR(generateTestCSR(t, "x"), h, time.Hour); err == nil {
			t.Errorf("SignCSR(%q) succeeded, want rejection", h)
		}
	}
}
