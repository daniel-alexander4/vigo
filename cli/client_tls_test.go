// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeServerCA writes the httptest server's self-signed certificate as a PEM
// CA bundle — it is its own issuer, so it is the trust anchor.
func writeServerCA(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	if err := os.WriteFile(p, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeUnrelatedCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "unrelated CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "ca")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func setTLSFlags(t *testing.T, caFile string, insecure bool) {
	t.Helper()
	oldCA, oldInsecure := caFileFlag, insecureTLS
	caFileFlag, insecureTLS = caFile, insecure
	t.Setenv("VIGO_CA_FILE", "")
	t.Cleanup(func() { caFileFlag, insecureTLS = oldCA, oldInsecure })
}

func getStatus(t *testing.T, baseURL string) (int, error) {
	t.Helper()
	resp, err := newCLIHTTPClient(baseURL, 5*time.Second).Get(baseURL + "/")
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// TestCLITLSLoopbackPinsServerCA: a loopback https URL is verified against the
// server CA, not skipped. The token-collecting impostor (any cert not chained
// to the CA) is refused, and so is a missing CA file.
func TestCLITLSLoopbackPinsServerCA(t *testing.T) {
	var hits int
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer ts.Close()
	if !isLocalhost(ts.URL) {
		t.Fatalf("test precondition: %s is not loopback", ts.URL)
	}

	t.Run("right CA verifies", func(t *testing.T) {
		setTLSFlags(t, writeServerCA(t, ts), false)
		if code, err := getStatus(t, ts.URL); err != nil || code != http.StatusOK {
			t.Fatalf("pinned CA: code=%d err=%v", code, err)
		}
	})

	t.Run("impostor cert refused", func(t *testing.T) {
		// Every httptest TLS server shares one built-in cert, so the
		// "wrong" CA is a freshly minted, unrelated self-signed cert.
		setTLSFlags(t, writeUnrelatedCA(t), false)
		before := hits
		_, err := getStatus(t, ts.URL)
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("cert not chained to the pinned CA must be refused, got err=%v", err)
		}
		if hits != before {
			t.Fatal("request reached the impostor")
		}
	})

	t.Run("unreadable CA refuses to send", func(t *testing.T) {
		setTLSFlags(t, filepath.Join(t.TempDir(), "absent"), false)
		before := hits
		_, err := getStatus(t, ts.URL)
		if err == nil || !strings.Contains(err.Error(), "cannot read the server CA") {
			t.Fatalf("missing CA must be an error, got %v", err)
		}
		if hits != before {
			t.Fatal("request was sent without a trust anchor")
		}
	})

	t.Run("VIGO_CA_FILE is honoured", func(t *testing.T) {
		setTLSFlags(t, "", false)
		t.Setenv("VIGO_CA_FILE", writeServerCA(t, ts))
		if code, err := getStatus(t, ts.URL); err != nil || code != http.StatusOK {
			t.Fatalf("env CA: code=%d err=%v", code, err)
		}
	})

	t.Run("--insecure skips verification", func(t *testing.T) {
		setTLSFlags(t, filepath.Join(t.TempDir(), "absent"), true)
		if code, err := getStatus(t, ts.URL); err != nil || code != http.StatusOK {
			t.Fatalf("--insecure: code=%d err=%v", code, err)
		}
	})
}

// TestCLITLSPlainHTTPUntouched: http:// needs no CA (the in-package client
// tests run against httptest.NewServer on 127.0.0.1).
func TestCLITLSPlainHTTPUntouched(t *testing.T) {
	setTLSFlags(t, filepath.Join(t.TempDir(), "absent"), false)
	cfg, err := cliTLSConfig("http://127.0.0.1:1")
	if err != nil || cfg != nil {
		t.Fatalf("plain http: cfg=%v err=%v", cfg, err)
	}
}
