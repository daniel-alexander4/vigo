// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/metrics"
)

// TestInterceptorRefusesRevokedEnvoy: a revoked envoy still holds a valid
// ed25519 key and a CA-signed cert, so the revoked bit is the only thing that
// stops it. Every signed unary agent RPC must be refused with PermissionDenied
// on BOTH lookup paths — the fleet-index fast path and the DB slow path — and
// the same envoy un-revoked must pass (control), so the refusal is the revoked
// bit and nothing else.
func TestInterceptorRefusesRevokedEnvoy(t *testing.T) {
	rpcs := []struct {
		name   string
		method string
		build  func(envoyID string, priv []byte) any
	}{
		{"CheckIn", pb.VigoAgent_CheckIn_FullMethodName, func(id string, k []byte) any {
			return signCheckIn(id, "fp", time.Now(), k)
		}},
		{"ReportResult", pb.VigoAgent_ReportResult_FullMethodName, func(id string, k []byte) any {
			return signReportResult(id, "run-1", time.Now(), k)
		}},
		{"ReportTraits", pb.VigoAgent_ReportTraits_FullMethodName, func(id string, k []byte) any {
			return signReportTraits(id, []byte(`{"os":"linux"}`), time.Now(), k)
		}},
		{"ResolveSecrets", pb.VigoAgent_ResolveSecrets_FullMethodName, func(id string, k []byte) any {
			refs := []string{"vigo/b", "vigo/a"}
			ts := fmt.Sprintf("%d", time.Now().Unix())
			sig := signPayload([]byte(strings.Join([]string{"vigo/a", "vigo/b"}, "\x00")), ts, k)
			return &pb.ResolveSecretsRequest{EnvoyId: id, Refs: refs, Timestamp: ts, Signature: sig}
		}},
		{"Wake", pb.VigoAgent_Wake_FullMethodName, func(id string, k []byte) any {
			return signWake(id, time.Now(), k)
		}},
	}

	for _, rpc := range rpcs {
		for _, path := range []string{"fast", "slow"} {
			for _, revoked := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/revoked=%v", rpc.name, path, revoked)
				t.Run(name, func(t *testing.T) {
					pub, priv, pubPEM := genKeyPair(t)
					queries := db.New(testDB)
					ctx := context.Background()
					fi := fleet.NewFleetIndex()
					var envoyID string
					if path == "fast" {
						envoyID = uuid.New().String()
						fi.AddEnvoy(fleet.EnvoyState{ID: envoyID, Hostname: "rev-" + envoyID[:8] + ".example.com", PubkeyPEM: pubPEM, Revoked: revoked}, pub)
					} else {
						e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
							Hostname:  "rev-" + uuid.New().String()[:8] + ".example.com",
							IpAddress: "10.98.0.1",
							PubkeyPem: pubPEM,
						})
						if err != nil {
							t.Fatalf("CreateEnvoy: %v", err)
						}
						envoyID = e.ID
						if revoked {
							if err := queries.RevokeEnvoy(ctx, envoyID); err != nil {
								t.Fatalf("RevokeEnvoy: %v", err)
							}
						}
					}

					interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi, metrics.NewTestMetrics())
					called := false
					_, err := interceptor(ctx, rpc.build(envoyID, priv), makeInfo(rpc.method), func(ctx context.Context, r any) (any, error) {
						called = true
						return r, nil
					})
					if !revoked {
						if err != nil || !called {
							t.Fatalf("control (not revoked): err=%v called=%v, want pass", err, called)
						}
						return
					}
					if called {
						t.Fatal("handler ran for a revoked envoy")
					}
					if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
						t.Fatalf("revoked envoy: got %v, want PermissionDenied", err)
					}
				})
			}
		}
	}
}
