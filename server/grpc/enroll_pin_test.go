// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
	"git.alexander4.io/vigo/server/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func peerCtxWithSPKI(spki []byte) context.Context {
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{{RawSubjectPublicKeyInfo: spki}}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
}

func testPubPEM(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func spkiPin(spki []byte) string {
	sum := sha256.Sum256(spki)
	return hex.EncodeToString(sum[:])
}

// TestRegister_StoresCertPin: only bootstrap ever set a cert pin, so an envoy
// enrolled through gRPC Register stayed unpinned (enforceCertPin skipped it)
// forever. Register must pin the client certificate it arrived on, in both
// the DB row and the FleetIndex the verify fast path reads.
func TestRegister_StoresCertPin(t *testing.T) {
	ctx := peerCtxWithSPKI([]byte("register-cert-spki"))
	queries := db.New(testutil.OpenMigratedTestDB(t))
	fi := fleet.NewFleetIndex()
	reg := registry.NewRegistry(queries)
	reg.SetFleetIndex(fi)
	if _, err := reg.AddApprovedPattern(context.Background(), "*.pin.example.com", "pin-token"); err != nil {
		t.Fatal(err)
	}
	svc := &AgentService{registry: reg, queries: queries, fleetIndex: fi, metrics: metrics.NewTestMetrics()}

	resp, err := svc.Register(ctx, &pb.RegisterRequest{Hostname: "a.pin.example.com", IpAddress: "10.0.0.2", PubkeyPem: testPubPEM(t), Token: "pin-token"})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("Register: resp=%v err=%v", resp, err)
	}
	want := spkiPin([]byte("register-cert-spki"))
	row, err := queries.GetEnvoyByID(context.Background(), resp.GetEnvoyId())
	if err != nil {
		t.Fatal(err)
	}
	if row.CertSpkiSha256 != want {
		t.Errorf("DB cert pin = %q, want %q", row.CertSpkiSha256, want)
	}
	if ts, ok, _ := fi.GetEnvoyLite(resp.GetEnvoyId()); !ok || ts.CertSPKI != want {
		t.Errorf("FleetIndex cert pin = %q, want %q", ts.CertSPKI, want)
	}
}

// TestEnrollEnvoy_StoresCertPin: a routed enrollment reached the owning bolt
// with no SPKI, so routed envoys were never pinned. The pin now rides
// EnrollEnvoyRequest.cert_spki_sha256; a malformed one is refused.
func TestEnrollEnvoy_StoresCertPin(t *testing.T) {
	queries := db.New(testutil.OpenMigratedTestDB(t))
	reg := registry.NewRegistry(queries)
	admin := &AdminService{registry: reg, queries: queries}
	pin := spkiPin([]byte("routed-cert-spki"))

	resp, err := admin.EnrollEnvoy(context.Background(), &pb.EnrollEnvoyRequest{
		Hostname: "routed.pin.example.com", IpAddress: "10.0.0.3", PubkeyPem: testPubPEM(t), CertSpkiSha256: pin,
	})
	if err != nil {
		t.Fatalf("EnrollEnvoy: %v", err)
	}
	row, err := queries.GetEnvoyByID(context.Background(), resp.GetEnvoyId())
	if err != nil {
		t.Fatal(err)
	}
	if row.CertSpkiSha256 != pin {
		t.Errorf("routed envoy cert pin = %q, want %q", row.CertSpkiSha256, pin)
	}

	_, err = admin.EnrollEnvoy(context.Background(), &pb.EnrollEnvoyRequest{
		Hostname: "bad.pin.example.com", PubkeyPem: testPubPEM(t), CertSpkiSha256: strings.ToUpper(pin),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("malformed pin: err = %v, want InvalidArgument", err)
	}
}
