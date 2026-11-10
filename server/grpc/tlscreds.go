// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"net"

	"git.alexander4.io/vigo/server/metrics"
	"google.golang.org/grpc/credentials"
)

// countingCreds wraps a TransportCredentials to count completed
// ServerHandshakes by outcome. The underlying handshake (TLS 1.3 + mTLS
// verify) happens inside inner.ServerHandshake; this wrapper only
// observes the return — overhead is one labelled counter Inc per
// connection.
//
// Counts handshakes on the gRPC listener only (port 1530). The REST and
// curator-S3 listeners use net/http's TLS path, which doesn't surface
// an analogous hook — they're low-volume admin traffic and don't share
// the steady-state CPU cost we're measuring.
type countingCreds struct {
	inner credentials.TransportCredentials
	m     *metrics.Metrics
}

func newCountingCreds(inner credentials.TransportCredentials, m *metrics.Metrics) credentials.TransportCredentials {
	if m == nil {
		return inner
	}
	return &countingCreds{inner: inner, m: m}
}

func (c *countingCreds) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	nc, ai, err := c.inner.ServerHandshake(conn)
	status := "success"
	if err != nil {
		status = "fail"
	}
	c.m.GRPCTLSHandshakesTotal.WithLabelValues(status).Inc()
	return nc, ai, err
}

func (c *countingCreds) ClientHandshake(ctx context.Context, authority string, conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return c.inner.ClientHandshake(ctx, authority, conn)
}

func (c *countingCreds) Info() credentials.ProtocolInfo { return c.inner.Info() }

func (c *countingCreds) Clone() credentials.TransportCredentials {
	return &countingCreds{inner: c.inner.Clone(), m: c.m}
}

func (c *countingCreds) OverrideServerName(name string) error {
	return c.inner.OverrideServerName(name) //nolint:staticcheck // matches inner interface
}
