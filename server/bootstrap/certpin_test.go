// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"testing"
)

// TestCertPinFromPEM pins that the enrollment-time pin equals the hex sha256 of
// the certificate's SubjectPublicKeyInfo (what the verify path recomputes from
// the live TLS peer cert), and that malformed input is a clean error.
func TestCertPinFromPEM(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "web-01"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	cert, _ := x509.ParseCertificate(der)
	want := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	got, err := certPinFromPEM(certPEM)
	if err != nil {
		t.Fatalf("certPinFromPEM: %v", err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("pin = %s, want %s", got, hex.EncodeToString(want[:]))
	}

	if _, err := certPinFromPEM([]byte("not a pem")); err == nil {
		t.Error("certPinFromPEM(garbage) = nil error, want failure")
	}
}
