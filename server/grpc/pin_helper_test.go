// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"git.alexander4.io/vigo/server/registry"
)

// testPinSPKI is the SubjectPublicKeyInfo of the synthetic client cert that
// pinnedCtx presents; testPin is its enrolled pin. The verify path refuses an
// envoy with no pin, so interceptor fixtures enroll testPin and call through
// pinnedCtx.
var (
	testPinSPKI = []byte("vigo-test-enrolled-client-cert-spki")
	testPin     = func() string {
		sum := sha256.Sum256(testPinSPKI)
		return hex.EncodeToString(sum[:])
	}()
)

// pinnedCtx returns a context carrying an mTLS peer whose client cert matches
// testPin, from source address 192.0.2.10.
func pinnedCtx() context.Context {
	return pinnedCtxFrom("192.0.2.10")
}

// pinnedCtxFrom is pinnedCtx from the given source IP.
func pinnedCtxFrom(ip string) context.Context {
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{{RawSubjectPublicKeyInfo: testPinSPKI}}}
	return peer.NewContext(context.Background(), &peer.Peer{
		Addr:     &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000},
		AuthInfo: credentials.TLSInfo{State: state},
	})
}

// clientCertPin returns the enrolled-pin form (hex sha256 of the SPKI) of the
// test client cert dialTLS presents.
func clientCertPin(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testCertDir, "client.crt"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("client.crt: no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// pinClientCert enrolls the test client cert's pin for envoyID, as bootstrap /
// Register do for a real enrollment — the verify path refuses an unpinned envoy.
func pinClientCert(t *testing.T, reg *registry.Registry, envoyID string) {
	t.Helper()
	if err := reg.SetEnvoyCertPin(context.Background(), envoyID, clientCertPin(t)); err != nil {
		t.Fatalf("SetEnvoyCertPin: %v", err)
	}
}
