// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
)

// startTestServerWithConfig creates a gRPC server with the given config directory.
func startTestServerWithConfig(t *testing.T, configDir string) (string, *config.ConfigStore, func()) {
	t.Helper()
	ctx := context.Background()

	// Clean tables.
	for _, table := range []string{"run_results", "runs", "envoy_traits", "plans", "plan_results", "file_snapshots", "approved_patterns", "envoys"} {
		if _, err := testDB.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("failed to delete from %s: %v", table, err)
		}
	}

	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	configStore, err := config.NewConfigStore(configDir, &noopProvider{}, true)
	if err != nil {
		t.Fatal(err)
	}

	srv, _, _, _, _, _, err := isgrpc.NewServer(isgrpc.ServerConfig{
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

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go srv.Serve(lis)

	return lis.Addr().String(), configStore, func() { srv.Stop() }
}

// setupE2EConfig creates a temp config directory with the given modules and envoy entries.
func setupE2EConfig(t *testing.T, configcrates map[string]string, envoysYAML string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "examples", "stacks")
	for _, sub := range []string{"configcrates", "envoys", "roles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range configcrates {
		if err := os.WriteFile(filepath.Join(dir, "configcrates", name+".vgo"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if envoysYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, "envoys", "envoys.vgo"), []byte(envoysYAML), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// registerEnvoy is a helper that registers a envoy and returns its ID, keys, and agent client.
func registerEnvoy(t *testing.T, addr, hostname, token string) (string, ed25519.PublicKey, ed25519.PrivateKey, pb.VigoAgentClient) {
	t.Helper()
	ctx := context.Background()
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)

	_, err := reg.AddApprovedPattern(ctx, "*.test.example.com", token)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}

	envoyID, err := reg.Register(ctx, hostname, "10.0.0.1", pubPEM, token, false)
	if err != nil {
		t.Fatal(err)
	}

	conn := dialTLS(t, addr)
	t.Cleanup(func() { conn.Close() })
	client := pb.NewVigoAgentClient(conn)

	return envoyID, pub, priv, client
}

// doCheckIn performs a signed CheckIn RPC. Pass policyVersion=0 for first check-in
// (agent has no cached policy). Pass a non-zero value to simulate an agent that
// already has a cached policy at that version.
func doCheckIn(t *testing.T, client pb.VigoAgentClient, envoyID string, priv ed25519.PrivateKey, fingerprint string, policyVersion uint64) (*pb.CheckInResponse, error) {
	t.Helper()
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())
	payload := []byte(fingerprint)
	sig := signPayload(payload, tsStr, priv)

	return client.CheckIn(context.Background(), &pb.CheckInRequest{
		EnvoyId:          envoyID,
		StateFingerprint: fingerprint,
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
		PolicyVersion:    policyVersion,
	})
}

// TestE2E_RegisterCheckInFlow verifies the full register-then-check-in path:
// a envoy registers, checks in, and receives the expected policy bundle
// with the configured module.
func TestE2E_RegisterCheckInFlow(t *testing.T) {
	configcrates := map[string]string{
		"nginx": `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`,
	}
	envoys := `
envoys:
  - match: "*.test.example.com"
    configcrates: [nginx]
`
	configDir := setupE2EConfig(t, configcrates, envoys)
	addr, _, cleanup := startTestServerWithConfig(t, configDir)
	defer cleanup()

	envoyID, _, priv, client := registerEnvoy(t, addr, "e2e-web01.test.example.com", "e2e-register-token")

	resp, err := doCheckIn(t, client, envoyID, priv, "initial-fp", 0)
	if err != nil {
		t.Fatalf("CheckIn: %v", err)
	}

	bundle := resp.GetPolicyBundle()
	if bundle == nil {
		t.Fatal("expected non-nil PolicyBundle")
	}
	if len(bundle.GetConfigcrates()) != 1 {
		t.Fatalf("expected 1 module in bundle, got %d", len(bundle.GetConfigcrates()))
	}
	if bundle.GetConfigcrates()[0].GetName() != "nginx" {
		t.Errorf("module name = %q, want nginx", bundle.GetConfigcrates()[0].GetName())
	}
}

// TestE2E_StreamRequiresSignature verifies the AgentStream is ed25519-verified
// end to end: a correctly-signed handshake opens the stream, a bad-sig
// multiplexed check-in tears it down, and an unsigned handshake is rejected.
// This closes the cross-envoy impersonation gap where any enrolled mTLS client
// could open a stream as any envoy_id (mTLS proves a CA-signed cert, not identity).
func TestE2E_StreamRequiresSignature(t *testing.T) {
	configDir := setupE2EConfig(t, map[string]string{"nginx": "name: nginx\nresources: []\n"}, `
envoys:
  - match: "*.test.example.com"
    configcrates: [nginx]
`)
	addr, _, cleanup := startTestServerWithConfig(t, configDir)
	defer cleanup()

	envoyID, _, priv, client := registerEnvoy(t, addr, "e2e-stream01.test.example.com", "e2e-stream-token")

	signedCheckIn := func() *pb.CheckInRequest {
		ts := time.Now()
		return &pb.CheckInRequest{
			EnvoyId:          envoyID,
			StateFingerprint: "fp",
			Signature:        signPayload([]byte("fp"), fmt.Sprintf("%d", ts.Unix()), priv),
			Timestamp:        timestamppb.New(ts),
		}
	}

	// Signed handshake opens the stream; a bad-sig multiplexed check-in tears it down.
	stream, err := client.AgentStream(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := stream.Send(&pb.AgentStreamMessage{Msg: &pb.AgentStreamMessage_CheckIn{CheckIn: signedCheckIn()}}); err != nil {
		t.Fatalf("send signed handshake: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("signed handshake rejected: %v", err)
	}
	if resp.GetCheckInResponse() == nil {
		t.Fatalf("expected check-in response, got %T", resp.GetMsg())
	}
	bad := signedCheckIn()
	bad.Signature = []byte("garbage")
	if err := stream.Send(&pb.AgentStreamMessage{Msg: &pb.AgentStreamMessage_CheckIn{CheckIn: bad}}); err != nil {
		t.Fatalf("send bad-sig check-in: %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("bad-sig multiplexed check-in: got code %v, want Unauthenticated", status.Code(err))
	}

	// A fresh stream whose handshake carries no signature is rejected at open.
	stream2, err := client.AgentStream(context.Background())
	if err != nil {
		t.Fatalf("open stream2: %v", err)
	}
	unsigned := signedCheckIn()
	unsigned.Signature = nil
	if err := stream2.Send(&pb.AgentStreamMessage{Msg: &pb.AgentStreamMessage_CheckIn{CheckIn: unsigned}}); err != nil {
		t.Fatalf("send unsigned handshake: %v", err)
	}
	if _, err := stream2.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unsigned handshake: got code %v, want Unauthenticated", status.Code(err))
	}
}

// TestE2E_ConfigChangeVersionBump verifies that adding a module and reloading
// config produces a bumped PolicyVersion and includes both modules.
func TestE2E_ConfigChangeVersionBump(t *testing.T) {
	configcrates := map[string]string{
		"nginx": `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`,
	}
	envoys := `
envoys:
  - match: "*.test.example.com"
    configcrates: [nginx]
`
	configDir := setupE2EConfig(t, configcrates, envoys)
	addr, configStore, cleanup := startTestServerWithConfig(t, configDir)
	defer cleanup()

	envoyID, _, priv, client := registerEnvoy(t, addr, "e2e-version.test.example.com", "e2e-version-token")

	// First CheckIn -- get initial version.
	resp1, err := doCheckIn(t, client, envoyID, priv, "fp-v1", 0)
	if err != nil {
		t.Fatalf("CheckIn v1: %v", err)
	}
	v1 := resp1.GetPolicyVersion()
	bundle1 := resp1.GetPolicyBundle()
	if bundle1 == nil || len(bundle1.GetConfigcrates()) != 1 {
		t.Fatalf("v1: expected 1 module in bundle, got %v", bundle1)
	}

	// Add a new module and update envoy config.
	if err := os.WriteFile(filepath.Join(configDir, "configcrates", "certbot.vgo"), []byte(`
name: certbot
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "envoys", "envoys.vgo"), []byte(`
envoys:
  - match: "*.test.example.com"
    configcrates: [nginx, certbot]
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Reload config.
	if err := configStore.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Second CheckIn -- should have new module and bumped version.
	resp2, err := doCheckIn(t, client, envoyID, priv, "fp-v2", 0)
	if err != nil {
		t.Fatalf("CheckIn v2: %v", err)
	}

	bundle2 := resp2.GetPolicyBundle()
	if bundle2 == nil || len(bundle2.GetConfigcrates()) != 2 {
		t.Fatalf("v2: expected 2 modules in bundle, got %v", bundle2)
	}
	if resp2.GetPolicyVersion() <= v1 {
		t.Errorf("expected version bump: v1=%d, v2=%d", v1, resp2.GetPolicyVersion())
	}
}

// TestE2E_ForcePushDelivery verifies that setting force_push on a envoy
// prevents the no-change short-circuit, causing the server to deliver a
// full PolicyBundle even when the agent's cached policy version matches.
func TestE2E_ForcePushDelivery(t *testing.T) {
	configcrates := map[string]string{
		"nginx": `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`,
	}
	envoys := `
envoys:
  - match: "*.test.example.com"
    configcrates: [nginx]
`
	configDir := setupE2EConfig(t, configcrates, envoys)
	addr, _, cleanup := startTestServerWithConfig(t, configDir)
	defer cleanup()

	envoyID, _, priv, client := registerEnvoy(t, addr, "e2e-forcepush.test.example.com", "e2e-force-token")

	// First CheckIn -- get the current policy version.
	resp1, err := doCheckIn(t, client, envoyID, priv, "fp-init", 0)
	if err != nil {
		t.Fatalf("initial CheckIn: %v", err)
	}
	pv := resp1.GetPolicyVersion()
	if resp1.GetPolicyBundle() == nil {
		t.Fatal("expected PolicyBundle on first check-in")
	}

	// Second CheckIn with matching PolicyVersion -- should short-circuit.
	resp2, err := doCheckIn(t, client, envoyID, priv, "fp-cached", pv)
	if err != nil {
		t.Fatalf("cached CheckIn: %v", err)
	}
	if !resp2.GetNoChange() {
		t.Fatal("expected NoChange=true when policy version matches")
	}

	// Set force_push via DB.
	queries := db.New(testDB)
	if err := queries.SetForcePush(context.Background(), envoyID); err != nil {
		t.Fatalf("SetForcePush: %v", err)
	}

	// Third CheckIn with same PolicyVersion -- force_push should bypass short-circuit.
	resp3, err := doCheckIn(t, client, envoyID, priv, "fp-forced", pv)
	if err != nil {
		t.Fatalf("force-push CheckIn: %v", err)
	}
	if resp3.GetNoChange() {
		t.Error("expected NoChange=false after force push, but got true")
	}
	if resp3.GetPolicyBundle() == nil {
		t.Error("expected non-nil PolicyBundle after force push")
	}
}

// TestE2E_RevocationFlow verifies that a revoked envoy receives
// PermissionDenied on subsequent check-in attempts.
func TestE2E_RevocationFlow(t *testing.T) {
	configDir := setupE2EConfig(t, nil, "")
	addr, _, cleanup := startTestServerWithConfig(t, configDir)
	defer cleanup()

	envoyID, _, priv, client := registerEnvoy(t, addr, "e2e-revoke.test.example.com", "e2e-revoke-token")

	// CheckIn should succeed.
	_, err := doCheckIn(t, client, envoyID, priv, "fp-ok", 0)
	if err != nil {
		t.Fatalf("pre-revoke CheckIn: %v", err)
	}

	// Revoke.
	queries := db.New(testDB)
	reg := registry.NewRegistry(queries)
	if err := reg.RevokeEnvoy(context.Background(), envoyID); err != nil {
		t.Fatalf("RevokeEnvoy: %v", err)
	}

	// CheckIn should fail.
	_, err = doCheckIn(t, client, envoyID, priv, "fp-revoked", 0)
	if err == nil {
		t.Fatal("expected CheckIn to fail after revocation")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v: %s", st.Code(), st.Message())
	}
}
