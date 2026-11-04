// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
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
	"google.golang.org/grpc/status"
)

// handshakeOnlyStream feeds one TunnelHandshake and then EOF.
type handshakeOnlyStream struct {
	grpc.ServerStream
	hs   *pb.TunnelHandshake
	used bool
}

func (f *handshakeOnlyStream) Context() context.Context { return context.Background() }
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
// as every other agent RPC — revocation on both the fleet-index and DB paths,
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

	cases := []struct {
		name    string
		path    string // fast | slow
		revoked bool
		age     time.Duration
		want    codes.Code
	}{
		{"fast valid", "fast", false, 0, codes.FailedPrecondition},
		{"fast revoked", "fast", true, 0, codes.PermissionDenied},
		{"fast stale timestamp", "fast", false, time.Hour, codes.Unauthenticated},
		{"slow valid", "slow", false, 0, codes.FailedPrecondition},
		{"slow revoked", "slow", true, 0, codes.PermissionDenied},
		{"slow stale timestamp", "slow", false, time.Hour, codes.Unauthenticated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			fi := fleet.NewFleetIndex()
			var envoyID string
			if c.path == "fast" {
				envoyID = uuid.NewString()
				fi.AddEnvoy(fleet.EnvoyState{ID: envoyID, Hostname: "tun-" + envoyID[:8], PubkeyPEM: pubPEM, Revoked: c.revoked}, pub)
			} else {
				e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "tun-" + uuid.NewString()[:8], IpAddress: "10.0.0.9", PubkeyPem: pubPEM})
				if err != nil {
					t.Fatalf("CreateEnvoy: %v", err)
				}
				envoyID = e.ID
				if c.revoked {
					if err := queries.RevokeEnvoy(ctx, envoyID); err != nil {
						t.Fatal(err)
					}
				}
			}
			svc := &AgentService{
				queries:         queries,
				fleetIndex:      fi,
				metrics:         metrics.NewTestMetrics(),
				signatureWindow: 5 * time.Minute,
				tunnelMgr:       NewTunnelManager("", "", nil, 1, nil, fi),
			}
			err := svc.TunnelStream(&handshakeOnlyStream{hs: tunnelHandshake(envoyID, time.Now().Add(-c.age), priv)})
			if got := status.Code(err); got != c.want {
				t.Fatalf("TunnelStream: got %v (%v), want %v", got, err, c.want)
			}
		})
	}
}
