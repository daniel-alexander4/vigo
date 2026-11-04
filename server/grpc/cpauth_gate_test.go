// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/cpauth"
	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/metrics"
)

// adminMethod is a representative privileged VigoAdmin RPC the network
// interceptor must gate. Use the generated FullMethodName constant — the real
// "/vigo.v1.VigoAdmin/EnrollEnvoy" — so this test exercises the actual wire
// method name, not a stale literal the runtime never produces.
const adminMethod = pb.VigoAdmin_EnrollEnvoy_FullMethodName

// cpauthCtx builds an incoming context carrying a control-plane signed call
// from the given identity (network path: no UDS peer).
func cpauthCtx(t *testing.T, priv ed25519.PrivateKey, pubHex string) context.Context {
	t.Helper()
	md, err := cpauth.Signer(priv, pubHex).GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	return metadata.NewIncomingContext(context.Background(), metadata.New(md))
}

func insertLiveBolt(t *testing.T, q *db.Queries, pubHex string) {
	t.Helper()
	if err := q.InsertBolt(context.Background(), db.InsertBoltParams{
		Pubkey:        pubHex,
		Hostname:      "bolt.example.com",
		AdmittedAt:    1000,
		AdmittedBy:    "test",
		PatternClaims: "[]",
		Signature:     []byte("test-admission-sig"),
	}); err != nil {
		t.Fatalf("InsertBolt: %v", err)
	}
}

// TestInterceptorVigoAdminUnsignedRejected: an unsigned VigoAdmin RPC over
// the network (no UDS peer, no cpauth metadata) is rejected and never reaches
// the handler.
func TestInterceptorVigoAdminUnsignedRejected(t *testing.T) {
	queries := db.New(testDB)
	ic := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, nil, metrics.NewTestMetrics(), nil)

	called := false
	h := func(ctx context.Context, req any) (any, error) { called = true; return req, nil }
	_, err := ic(context.Background(), &pb.EnrollEnvoyRequest{}, makeInfo(adminMethod), h)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
	if called {
		t.Fatal("handler must not run for unsigned admin RPC")
	}
}

// TestInterceptorVigoAdminValidRosterBolt: a call signed by an admitted
// roster bolt is authorized.
func TestInterceptorVigoAdminValidRosterBolt(t *testing.T) {
	pub, priv, _ := genKeyPair(t)
	pubHex := hex.EncodeToString(pub)
	queries := db.New(testDB)
	insertLiveBolt(t, queries, pubHex)
	ic := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, nil, metrics.NewTestMetrics(), nil)

	called := false
	h := func(ctx context.Context, req any) (any, error) { called = true; return req, nil }
	_, err := ic(cpauthCtx(t, priv, pubHex), &pb.EnrollEnvoyRequest{}, makeInfo(adminMethod), h)
	if err != nil {
		t.Fatalf("roster-bolt call should pass, got %v", err)
	}
	if !called {
		t.Fatal("handler should run for a valid roster-bolt call")
	}
}

// TestInterceptorVigoAdminNonRosterRejected: a validly signed call from a key
// that is NOT an admitted roster bolt is denied.
func TestInterceptorVigoAdminNonRosterRejected(t *testing.T) {
	pub, priv, _ := genKeyPair(t)
	pubHex := hex.EncodeToString(pub) // never inserted into the roster
	queries := db.New(testDB)
	ic := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, nil, metrics.NewTestMetrics(), nil)

	_, err := ic(cpauthCtx(t, priv, pubHex), &pb.EnrollEnvoyRequest{}, makeInfo(adminMethod), noopHandler)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}

// TestInterceptorVigoAdminUDSExempt: a VigoAdmin RPC arriving over the local
// operator UDS is exempt from the signed-call requirement (it is token+RBAC-
// authed at the REST layer).
func TestInterceptorVigoAdminUDSExempt(t *testing.T) {
	queries := db.New(testDB)
	ic := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, nil, metrics.NewTestMetrics(), nil)

	udsCtx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.UnixAddr{Name: "/run/vigo/admin.sock", Net: "unix"},
	})
	called := false
	h := func(ctx context.Context, req any) (any, error) { called = true; return req, nil }
	_, err := ic(udsCtx, &pb.EnrollEnvoyRequest{}, makeInfo(adminMethod), h)
	if err != nil {
		t.Fatalf("UDS operator admin RPC should pass, got %v", err)
	}
	if !called {
		t.Fatal("handler should run for UDS operator admin RPC")
	}
}
