// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package cpauth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestSignedMessageInjective: the length prefixes keep a byte moved between
// fields from producing the same preimage.
func TestSignedMessageInjective(t *testing.T) {
	a := signedMessage("1", "/a.B/C", []byte("x"))
	b := signedMessage("1", "/a.B/Cx", nil)
	if bytes.Equal(a, b) {
		t.Fatal("distinct (method, binding) pairs share a preimage")
	}
}

// testServer is a TLS gRPC server (health service) whose interceptors run
// Verify and record the cpauth metadata each call carried, so a test can
// replay it.
type testServer struct {
	addr string
	mu   sync.Mutex
	seen []metadata.MD
}

func (s *testServer) check(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	s.seen = append(s.seen, md.Copy())
	s.mu.Unlock()
	if _, err := Verify(ctx, time.Minute); err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	return nil
}

func (s *testServer) lastMD() metadata.MD {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[len(s.seen)-1]
}

func selfSignedTLS(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func startServer(t *testing.T, cert tls.Certificate) *testServer {
	t.Helper()
	ts := &testServer{}
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if err := ts.check(ctx); err != nil {
				return nil, err
			}
			return h(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			if err := ts.check(ss.Context()); err != nil {
				return err
			}
			return h(srv, ss)
		}),
	)
	healthpb.RegisterHealthServer(srv, health.NewServer())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	ts.addr = lis.Addr().String()
	return ts
}

func dial(t *testing.T, addr string, pool *x509.CertPool, opts ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "localhost"})))
	cc, err := grpc.NewClient(addr, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func signedConn(t *testing.T, addr string, pool *x509.CertPool) *grpc.ClientConn {
	pub, priv := mustKey(t)
	return dial(t, addr, pool, grpc.WithPerRPCCredentials(Signer(priv, hex.EncodeToString(pub))))
}

func healthCheck(cc *grpc.ClientConn, md metadata.MD) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if md != nil {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	_, err := healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

// cpauthOnly strips gRPC's own headers so a replay re-sends only the proof.
func cpauthOnly(md metadata.MD) metadata.MD {
	return metadata.Pairs(mdPubkey, md.Get(mdPubkey)[0], mdTS, md.Get(mdTS)[0], mdSig, md.Get(mdSig)[0])
}

// TestSignedCallOverTLSVerifies: a real signed call over TLS verifies, so
// the signer and verifier derive the same method and channel binding.
func TestSignedCallOverTLSVerifies(t *testing.T) {
	cert, pool := selfSignedTLS(t)
	srv := startServer(t, cert)
	if err := healthCheck(signedConn(t, srv.addr, pool), nil); err != nil {
		t.Fatalf("signed call rejected: %v", err)
	}
}

// TestReplayToAnotherServerRejected: a server that received a signed call
// replays the proof to a third server (same CA, same method, inside the
// window). The replay rides a different TLS connection and must fail.
func TestReplayToAnotherServerRejected(t *testing.T) {
	cert, pool := selfSignedTLS(t)
	victim := startServer(t, cert)
	third := startServer(t, cert)

	if err := healthCheck(signedConn(t, victim.addr, pool), nil); err != nil {
		t.Fatalf("original call: %v", err)
	}
	stolen := cpauthOnly(victim.lastMD())

	err := healthCheck(dial(t, third.addr, pool), stolen)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("replay to another server: want Unauthenticated, got %v", err)
	}
}

// TestReplayToAnotherMethodRejected: the proof for one method, replayed on
// the same connection to another method, must fail.
func TestReplayToAnotherMethodRejected(t *testing.T) {
	cert, pool := selfSignedTLS(t)
	srv := startServer(t, cert)

	// The proof is also connection-bound, so the replay must ride the
	// connection it was minted on: the first Check is signed, then the
	// interceptors re-send its captured proof on the same connection —
	// to Check (must still verify) and to Watch (must fail).
	pub, priv := mustKey(t)
	var replay metadata.MD
	cc := dial(t, srv.addr, pool, grpc.WithChainUnaryInterceptor(
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			if replay != nil {
				ctx = metadata.NewOutgoingContext(ctx, replay)
				return inv(ctx, method, req, reply, cc, opts...)
			}
			return inv(ctx, method, req, reply, cc, append(opts, grpc.PerRPCCredentials(Signer(priv, hex.EncodeToString(pub))))...)
		}),
		grpc.WithChainStreamInterceptor(
			func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, st grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
				return st(metadata.NewOutgoingContext(ctx, replay), desc, cc, method, opts...)
			}))

	if err := healthCheck(cc, nil); err != nil {
		t.Fatalf("original call: %v", err)
	}
	replay = cpauthOnly(srv.lastMD())

	// Same method, same connection, inside the window: still valid.
	if err := healthCheck(cc, nil); err != nil {
		t.Fatalf("same-method replay on the same connection should verify: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := healthpb.NewHealthClient(cc).Watch(ctx, &healthpb.HealthCheckRequest{})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("replay to another method: want Unauthenticated, got %v", err)
	}
}
