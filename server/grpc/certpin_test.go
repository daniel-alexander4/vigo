// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"testing"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// TestEnforceCertPin pins the cert-pinning enforcement: a connection whose TLS
// client-cert SPKI matches the enrolled pin is accepted; a different cert (a
// TLS-terminating proxy/MITM presenting its own CA-signed cert) is rejected
// Unauthenticated; an empty stored pin (grandfathered envoy) skips enforcement;
// and any uncertainty (no peer / no cert) fails closed.
func TestEnforceCertPin(t *testing.T) {
	m := metrics.NewTestMetrics()

	// A synthetic peer cert is identified solely by its SPKI bytes — that's all
	// enforceCertPin hashes, so we control both sides deterministically.
	ctxWithSPKI := func(spki []byte) context.Context {
		state := tls.ConnectionState{}
		if spki != nil {
			state.PeerCertificates = []*x509.Certificate{{RawSubjectPublicKeyInfo: spki}}
		}
		return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
	}
	pinOf := func(spki []byte) string {
		sum := sha256.Sum256(spki)
		return hex.EncodeToString(sum[:])
	}
	enrolled := []byte("the-enrolled-cert-spki")
	envoy := func(pin string) db.Envoy {
		return db.Envoy{ID: "e1", Hostname: "web-01", CertSpkiSha256: pin}
	}

	tests := []struct {
		name    string
		ctx     context.Context
		envoy   db.Envoy
		wantErr bool
	}{
		{"match accepted", ctxWithSPKI(enrolled), envoy(pinOf(enrolled)), false},
		{"mismatch rejected", ctxWithSPKI([]byte("a-proxy-cert-spki")), envoy(pinOf(enrolled)), true},
		{"grandfathered (empty pin) skips", ctxWithSPKI([]byte("anything")), envoy(""), false},
		{"no client cert fails closed", ctxWithSPKI(nil), envoy(pinOf(enrolled)), true},
		{"no peer fails closed", context.Background(), envoy(pinOf(enrolled)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforceCertPin(tt.ctx, tt.envoy, m)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("enforceCertPin = nil, want rejection")
				}
				if status.Code(err) != codes.Unauthenticated {
					t.Errorf("enforceCertPin code = %v, want Unauthenticated", status.Code(err))
				}
				return
			}
			if err != nil {
				t.Errorf("enforceCertPin = %v, want accept", err)
			}
		})
	}
}
