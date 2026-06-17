// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
)

// TestGatewayOperatorSocket pins the F4 regression one layer up from
// TestOperatorSocketPlaintext: it drives a real REST request through a
// grpc-gateway mux dialing the operator UDS, exactly as server/api/gateway.go
// does in production, rather than a raw gRPC client. TestOperatorSocketPlaintext
// proves the operatorSrv transport is plaintext; this proves the full
// REST → grpc-gateway → UDS → VigoAdmin round-trip succeeds.
//
// It is the only gateway-layer guard that runs in-process with no binary
// dependency: clismoke covers the same path end-to-end but skips when bin/ is
// unbuilt, and that silent skip is exactly what let the F4 regression ship.
//
// If operatorSrv is ever reverted to mTLS, the gateway's insecure dial fails the
// handshake → codes.Unavailable → HTTP 503, and this test goes red.
func TestGatewayOperatorSocket(t *testing.T) {
	ctx := context.Background()

	for _, table := range []string{"run_results", "runs", "envoy_traits", "plans", "plan_results", "file_snapshots", "approved_patterns", "envoys"} {
		if _, err := testDB.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}

	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)
	configStore, err := config.NewConfigStore(t.TempDir(), &noopProvider{}, true)
	if err != nil {
		t.Fatal(err)
	}

	_, operatorSrv, _, _, _, _, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:    filepath.Join(testCertDir, "server.crt"),
		KeyFile:     filepath.Join(testCertDir, "server.key"),
		CAFile:      filepath.Join(testCertDir, "ca.crt"),
		GRPCPort:    ":0",
		Registry:    reg,
		ConfigStore: configStore,
		Queries:     queries,
		Metrics:     metrics.NewTestMetrics(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Serve operatorSrv on a Unix-domain socket, as server_run.go does.
	sockPath := filepath.Join(t.TempDir(), "admin.sock")
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	go operatorSrv.Serve(lis)
	defer operatorSrv.Stop()

	// Build the grpc-gateway mux against the UDS with insecure credentials,
	// mirroring server/api/gateway.go's NewGateway.
	gwMux := runtime.NewServeMux()
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if err := pb.RegisterVigoAdminHandlerFromEndpoint(ctx, gwMux, "unix://"+sockPath, dialOpts); err != nil {
		t.Fatalf("register admin gateway: %v", err)
	}

	gw := httptest.NewServer(gwMux)
	defer gw.Close()

	// Drive a gateway-bridged VigoAdmin call over REST. ListEnvoys is a
	// cpauth-exempt unix-peer call, so over the empty DB it returns 200 with an
	// empty list. A 503 here is the F4 regression: a plaintext gateway dial
	// against an mTLS-served UDS.
	resp, err := http.Get(gw.URL + "/api/v1/envoys")
	if err != nil {
		t.Fatalf("GET /api/v1/envoys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("gateway → operator UDS returned %d (want 200) — the F4 regression (operator UDS served mTLS while the gateway dials plaintext): %s", resp.StatusCode, body)
	}

	// A valid JSON object proves the proto response round-tripped through the
	// gateway's JSON marshaling, not just that the transport connected.
	var got map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode gateway JSON response: %v", err)
	}
}
