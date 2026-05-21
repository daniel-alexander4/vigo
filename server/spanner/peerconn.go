// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Spanner Slice 2-ii-a — the peer-conn layer (ADR-026 + the
// spanner-redesign plan). This file lifts the bolt-to-bolt admin-gRPC
// dialing out of hub.go so it survives Slice 2-ii-b's deletion of the
// hub: the peer-equal `fanout` admin path (the kill-switch in
// spanner.fallback_admin) needs to dial every roster bolt's surviving
// per-bolt admin RPCs without a Hub to drive it.
//
// PeerConn is one long-lived mTLS gRPC connection to a peer bolt's
// VigoAdmin surface. PeerDialer builds the shared mTLS credentials
// once and hands out PeerConns. hub.go now USES PeerDialer rather than
// constructing connections itself — behaviour is identical, the
// dialing code just lives in one reusable place.

// PeerConn is one bolt-to-bolt admin-gRPC connection. It wraps the
// raw *grpc.ClientConn plus a ready-to-use VigoAdminClient. id and
// addr identify the peer; patterns is its operator-asserted hostname
// claim (display-only, never an authorization input).
type PeerConn struct {
	ID       string
	Addr     string
	Patterns []string
	Conn     *grpc.ClientConn
	Admin    pb.VigoAdminClient
}

// Close shuts the underlying gRPC connection. nil-safe.
func (p *PeerConn) Close() {
	if p != nil && p.Conn != nil {
		p.Conn.Close()
	}
}

// PeerDialer holds the shared mTLS client credentials for bolt-to-bolt
// admin gRPC and dials peer connections from them. One PeerDialer is
// constructed per vigosrv; PeerConns are dialed lazily (gRPC.NewClient
// never blocks — the TCP connection opens on the first RPC).
type PeerDialer struct {
	creds credentials.TransportCredentials
	mu    sync.Mutex
}

// NewPeerDialer builds the shared mTLS credentials for peer admin
// connections from this vigosrv's fleet-CA-signed cert/key/CA chain.
func NewPeerDialer(certFile, keyFile, caFile string) (*PeerDialer, error) {
	creds, err := buildClientTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("building peer TLS credentials: %w", err)
	}
	return &PeerDialer{creds: creds}, nil
}

// Dial opens a lazy long-lived gRPC client connection to a peer
// bolt's admin surface. addr is the gRPC target (host:port). id is a
// caller-supplied handle (the peer's pubkey-hex once known, or the
// addr as a temporary handle). NewClient never blocks and only errors
// on a malformed target — the connection opens on the first RPC and
// gRPC reconnects in the background.
func (d *PeerDialer) Dial(id, addr string, patterns []string) (*PeerConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(d.creds))
	if err != nil {
		return nil, fmt.Errorf("connecting to peer at %s: %w", addr, err)
	}
	return &PeerConn{
		ID:       id,
		Addr:     addr,
		Patterns: patterns,
		Conn:     conn,
		Admin:    pb.NewVigoAdminClient(conn),
	}, nil
}

// buildClientTLS creates TLS credentials for bolt-to-bolt admin gRPC.
// Shared by PeerDialer and the legacy Hub constructor — both dial the
// same fleet-CA-signed mTLS surface.
func buildClientTLS(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to add CA cert to pool")
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}), nil
}
