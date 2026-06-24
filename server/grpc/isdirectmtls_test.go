// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// TestIsDirectMTLS pins the TLS-terminating-proxy detector: a direct agent
// presents its own enrollment cert (CN or a DNS-SAN == the envoy's hostname) and
// is treated as direct; a proxy re-originating TLS presents a different cert and
// is not. Any uncertainty (no peer / no TLS info / no cert) is fail-safe → false.
func TestIsDirectMTLS(t *testing.T) {
	ctxWithCert := func(cert *x509.Certificate) context.Context {
		state := tls.ConnectionState{}
		if cert != nil {
			state.PeerCertificates = []*x509.Certificate{cert}
		}
		return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
	}
	certWith := func(cn string, dns ...string) *x509.Certificate {
		return &x509.Certificate{Subject: pkix.Name{CommonName: cn}, DNSNames: dns}
	}

	tests := []struct {
		name     string
		ctx      context.Context
		hostname string
		want     bool
	}{
		{"cn matches", ctxWithCert(certWith("web-01")), "web-01", true},
		{"dns san matches", ctxWithCert(certWith("ignored", "web-01")), "web-01", true},
		{"proxy cert mismatch", ctxWithCert(certWith("lb.internal", "lb.internal")), "web-01", false},
		{"tls but no cert", ctxWithCert(nil), "web-01", false},
		{"no peer info", context.Background(), "web-01", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDirectMTLS(tt.ctx, tt.hostname); got != tt.want {
				t.Errorf("isDirectMTLS(%q) = %v, want %v", tt.hostname, got, tt.want)
			}
		})
	}
}

// TestProxyStreamDecision pins the all-modes terminating-proxy handling: a proxy
// is detected (metered + warned) in BOTH `all` and `handshake-only`, a direct
// connection never trips it, and handshake-only relaxation is granted only on a
// direct connection (proxy → fall back to full per-message verification).
func TestProxyStreamDecision(t *testing.T) {
	tests := []struct {
		name                    string
		relaxConfigured, direct bool
		wantRelax, wantDetected bool
	}{
		{"all mode, direct", false, true, false, false},
		{"all mode, proxy — detected even though all never relaxes", false, false, false, true},
		{"handshake-only, direct — relaxation granted", true, true, true, false},
		{"handshake-only, proxy — relaxation withdrawn, detected", true, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relax, detected := proxyStreamDecision(tt.relaxConfigured, tt.direct)
			if relax != tt.wantRelax || detected != tt.wantDetected {
				t.Errorf("proxyStreamDecision(relax=%v, direct=%v) = (relax %v, detected %v), want (relax %v, detected %v)",
					tt.relaxConfigured, tt.direct, relax, detected, tt.wantRelax, tt.wantDetected)
			}
		})
	}
}
