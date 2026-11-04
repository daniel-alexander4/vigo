// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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
	"fmt"
	"io"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/testutil"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// handshakeOnlyStream feeds one TunnelHandshake and then EOF.
type handshakeOnlyStream struct {
	grpc.ServerStream
	hs   *pb.TunnelHandshake
	used bool
	ctx  context.Context
}

func (f *handshakeOnlyStream) Context() context.Context {
	if f.ctx != nil {
		return f.ctx
	}
	return context.Background()
}
func (f *handshakeOnlyStream) Send(*pb.TunnelServerMessage) error {
	return nil
}
func (f *handshakeOnlyStream) Recv() (*pb.TunnelClientMessage, error) {
	if f.used {
		return nil, io.EOF
	}
	f.used = true
	return &pb.TunnelClientMessage{Msg: &pb.TunnelClientMessage_Handshake{Handshake: f.hs}}, nil
}

func tunnelHandshake(envoyID string, ts time.Time, priv ed25519.PrivateKey) *pb.TunnelHandshake {
	tsStr := fmt.Sprintf("%d", ts.Unix())
	msg := append([]byte(envoyID), tsStr...)
	return &pb.TunnelHandshake{EnvoyId: envoyID, Timestamp: tsStr, Signature: ed25519.Sign(priv, msg)}
}

// TestTunnelStreamHandshakeGates: the TunnelStream handshake runs the same gates
// as every other agent RPC — revocation on the fleet-index, revoked-id-set and
// no-index DB paths, the cert pin,
// and the signature-window timestamp check, so one observed handshake (or Wake,
// whose preimage is identical) can't be replayed forever to claim a pending
// scrier tunnel. A valid handshake passes verification and reaches tunnel
// delivery (FailedPrecondition: none pending here).
func TestTunnelStreamHandshakeGates(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	// The handshake arrives on an mTLS peer whose client cert matches the
	// envoy's enrolled pin.
	spki := []byte("tunnel-test-client-spki")
	sum := sha256.Sum256(spki)
	pin := hex.EncodeToString(sum[:])
	peerCtx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{RawSubjectPublicKeyInfo: spki}},
	}}})

	cases := []struct {
		name    string
		path    string // held | revokedSet | db
		revoked bool
		age     time.Duration
		want    codes.Code
	}{
		{"held valid", "held", false, 0, codes.FailedPrecondition},
		{"held revoked", "held", true, 0, codes.PermissionDenied},
		{"held stale timestamp", "held", false, time.Hour, codes.Unauthenticated},
		{"revoked set only (post-restart)", "revokedSet", true, 0, codes.PermissionDenied},
		{"db valid", "db", false, 0, codes.FailedPrecondition},
		{"db revoked", "db", true, 0, codes.PermissionDenied},
		{"db stale timestamp", "db", false, time.Hour, codes.Unauthenticated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			fi := fleet.NewFleetIndex()
			svcIdx := fi
			var envoyID string
			switch c.path {
			case "held":
				envoyID = uuid.NewString()
				fi.AddEnvoy(fleet.EnvoyState{ID: envoyID, Hostname: "tun-" + envoyID[:8], PubkeyPEM: pubPEM, CertSPKI: pin, Revoked: c.revoked}, pub)
			case "revokedSet":
				envoyID = uuid.NewString()
				fi.AddRevokedIDs([]string{envoyID})
			case "db":
				svcIdx = nil
				e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "tun-" + uuid.NewString()[:8], IpAddress: "10.0.0.9", PubkeyPem: pubPEM})
				if err != nil {
					t.Fatalf("CreateEnvoy: %v", err)
				}
				envoyID = e.ID
				if err := queries.UpdateEnvoyCertPin(ctx, db.UpdateEnvoyCertPinParams{CertSpkiSha256: pin, ID: envoyID}); err != nil {
					t.Fatal(err)
				}
				if c.revoked {
					if err := queries.RevokeEnvoy(ctx, envoyID); err != nil {
						t.Fatal(err)
					}
				}
			}
			svc := &AgentService{
				queries:         queries,
				fleetIndex:      svcIdx,
				metrics:         metrics.NewTestMetrics(),
				signatureWindow: 5 * time.Minute,
				tunnelMgr:       NewTunnelManager("", "", nil, 1, nil, fi),
			}
			err := svc.TunnelStream(&handshakeOnlyStream{ctx: peerCtx, hs: tunnelHandshake(envoyID, time.Now().Add(-c.age), priv)})
			if got := status.Code(err); got != c.want {
				t.Fatalf("TunnelStream: got %v (%v), want %v", got, err, c.want)
			}
		})
	}
}
