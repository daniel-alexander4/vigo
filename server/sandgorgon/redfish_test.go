// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package sandgorgon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A self-signed BMC must be rejected unless the operator opts into
// InsecureSkipVerify — the connection carries BMC credentials.
func TestRedfishClient_VerifiesBMCCertificateByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	m := NewManager(nil)
	if err := m.NewBMCClient(srv.URL, "admin", "pw").Ping(context.Background()); err == nil {
		t.Fatal("Ping to a self-signed BMC succeeded with default TLS policy; certificate was not verified")
	}

	m.SetBMCTLS(RedfishTLS{InsecureSkipVerify: true})
	if err := m.NewBMCClient(srv.URL, "admin", "pw").Ping(context.Background()); err != nil {
		t.Fatalf("Ping with explicit InsecureSkipVerify: %v", err)
	}
}

// A bsdtar that never exits must not hang image registration.
func TestExtractKernelFromISO_BsdtarIsBounded(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "bsdtar")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin, oldTimeout := bsdtarBin, isoToolTimeout
	bsdtarBin, isoToolTimeout = fake, 200*time.Millisecond
	t.Cleanup(func() { bsdtarBin, isoToolTimeout = oldBin, oldTimeout })

	start := time.Now()
	_, _, err := extractKernelFromISO(context.Background(), filepath.Join(dir, "x.iso"), "x")
	if err == nil {
		t.Fatal("extractKernelFromISO returned nil error from a hung bsdtar")
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("extractKernelFromISO took %s; bsdtar was not bounded by isoToolTimeout", el)
	}
}
