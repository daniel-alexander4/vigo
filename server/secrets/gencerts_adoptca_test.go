// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

// TestExportAdoptCA_RoundTrip verifies a joiner that adopts the founder's CA
// ends up with the SAME CA and a server cert that chains to it (option-3
// shared-fleet-CA establishment for spanner).
func TestExportAdoptCA_RoundTrip(t *testing.T) {
	founder := filepath.Join(t.TempDir(), "tls")
	joiner := filepath.Join(t.TempDir(), "tls")
	if err := EnsureTLSCerts(founder); err != nil {
		t.Fatalf("founder EnsureTLSCerts: %v", err)
	}
	if err := EnsureTLSCerts(joiner); err != nil {
		t.Fatalf("joiner EnsureTLSCerts: %v", err)
	}

	caPEM, caKeyPEM, err := ExportCA(founder)
	if err != nil {
		t.Fatalf("ExportCA: %v", err)
	}
	if err := AdoptCA(joiner, caPEM, caKeyPEM); err != nil {
		t.Fatalf("AdoptCA: %v", err)
	}

	// The joiner's CA is now byte-identical to the founder's.
	jCA, _ := os.ReadFile(filepath.Join(joiner, "ca"))
	if string(jCA) != string(caPEM) {
		t.Error("joiner CA cert not identical to founder's after AdoptCA")
	}

	// The joiner's re-signed server cert chains to the shared CA.
	caCertPEM, _ := os.ReadFile(filepath.Join(joiner, "ca"))
	srvCert, err := readCert(filepath.Join(joiner, "cert"))
	if err != nil {
		t.Fatalf("reading joiner server cert: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caCertPEM) {
		t.Fatal("appending shared CA to pool")
	}
	if _, err := srvCert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("joiner server cert does not chain to the adopted CA: %v", err)
	}

	// A bad bundle is rejected.
	if err := AdoptCA(joiner, []byte("not a cert"), caKeyPEM); err == nil {
		t.Error("AdoptCA accepted an invalid CA cert")
	}
}
