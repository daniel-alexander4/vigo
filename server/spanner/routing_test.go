// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package spanner

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/testutil"
	"google.golang.org/grpc"
)

// Spanner Slice 2-ii-b — peer-equal enrollment routing + action
// propagation tests. The OwningBolt matcher is exercised against a
// migrated DB; the action-propagation paths (force-push / reassign /
// drain) use the dialBoltFn stub injection point so the orchestration
// runs without an mTLS gRPC harness.

// insertRosterBolt seeds one spanner_bolts row for routing tests.
func insertRosterBolt(t *testing.T, q *db.Queries, pubkey, hostname string, admittedAt int64, claims string) {
	t.Helper()
	if err := q.InsertBolt(t.Context(), db.InsertBoltParams{
		Pubkey:        pubkey,
		Hostname:      hostname,
		AdmittedAt:    admittedAt,
		AdmittedBy:    pubkey,
		PatternClaims: claims,
		Signature:     []byte("sig"),
	}); err != nil {
		t.Fatalf("InsertBolt %s: %v", hostname, err)
	}
}

// --- Step 1: OwningBolt matcher ---

// TestOwningBoltADR002Match asserts the matcher honours the ADR-002
// hostcrate semantics — a positive glob match owns, a "-" deny rule
// does not, and an unmatched hostname has no owner.
func TestOwningBoltADR002Match(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")

	webHex := hexKey(0xAA)
	dbHex := hexKey(0xBB)
	insertRosterBolt(t, q, webHex, "web-bolt.example.com", 100, `["*.web.example.com"]`)
	insertRosterBolt(t, q, dbHex, "db-bolt.example.com", 200, `["-test.db.example.com","*.db.example.com"]`)

	cases := []struct {
		hostname   string
		wantPubkey string
		wantOwned  bool
	}{
		{"node1.web.example.com", webHex, true},
		{"node1.db.example.com", dbHex, true},
		{"test.db.example.com", "", false}, // "-" deny rule wins first
		{"node1.other.example.com", "", false},
	}
	for _, c := range cases {
		pubkey, _, owned := a.OwningBolt(context.Background(), c.hostname)
		if owned != c.wantOwned {
			t.Errorf("OwningBolt(%q) owned = %v, want %v", c.hostname, owned, c.wantOwned)
			continue
		}
		if owned && pubkey != c.wantPubkey {
			t.Errorf("OwningBolt(%q) pubkey = %q, want %q", c.hostname, pubkey, c.wantPubkey)
		}
	}
}

// TestOwningBoltOverlapTiebreak asserts that when two bolts both
// positively claim a hostname, the bolt with the OLDEST admitted_at
// wins — the deterministic tiebreak every bolt computes identically.
func TestOwningBoltOverlapTiebreak(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")

	olderHex := hexKey(0x01)
	newerHex := hexKey(0x02)
	// Both claim "*" — the older-admitted bolt must win regardless of
	// roster row order, so insert the newer one first.
	insertRosterBolt(t, q, newerHex, "newer.example.com", 5000, `["*"]`)
	insertRosterBolt(t, q, olderHex, "older.example.com", 1000, `["*"]`)

	pubkey, hostname, owned := a.OwningBolt(context.Background(), "anything.example.com")
	if !owned {
		t.Fatal("expected an owner for an overlapping claim")
	}
	if pubkey != olderHex {
		t.Errorf("overlap winner pubkey = %q, want the older-admitted %q", pubkey, olderHex)
	}
	if hostname != "older.example.com" {
		t.Errorf("overlap winner hostname = %q, want older.example.com", hostname)
	}
}

// TestOwningBoltRevokedExcluded — a revoked bolt is off the live
// roster and never owns a hostname even if its claims still match.
func TestOwningBoltRevokedExcluded(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")

	gone := hexKey(0x09)
	insertRosterBolt(t, q, gone, "gone.example.com", 100, `["*"]`)
	if err := q.RevokeBolt(t.Context(), db.RevokeBoltParams{
		Pubkey:    gone,
		RevokedAt: sql.NullInt64{Int64: 999, Valid: true},
		RevokedBy: sql.NullString{String: "op", Valid: true},
	}); err != nil {
		t.Fatalf("RevokeBolt: %v", err)
	}

	if _, _, owned := a.OwningBolt(context.Background(), "host.example.com"); owned {
		t.Error("a revoked bolt should not own any hostname")
	}
}

// TestOwnsLocally — OwningBolt resolving to the local bolt's own
// pubkey means no redirect.
func TestOwnsLocally(t *testing.T) {
	a := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "selfpubkey", FallbackAdminLocal, "1530")
	if !a.OwnsLocally("selfpubkey") {
		t.Error("OwnsLocally should be true for the own pubkey")
	}
	if a.OwnsLocally("otherpubkey") {
		t.Error("OwnsLocally should be false for a peer pubkey")
	}
}

// --- action-propagation stub harness ---

// recordingAdminClient is a pb.VigoAdminClient that records every
// action RPC the peer-equal propagation paths make, so a test can
// assert call ordering + arguments. Unset RPCs panic.
type recordingAdminClient struct {
	pb.VigoAdminClient
	mu sync.Mutex

	calls   []string // ordered RPC names, e.g. "GetEnvoy", "EnrollEnvoy"
	envoy   *pb.Envoy
	envoys  []*pb.Envoy
	newID   string
	notify  int32
	enrollR []*pb.EnrollEnvoyRequest
	reassR  []*pb.SetReassignmentRequest
	fpR     []*pb.ForcePushRequest
	delR    []*pb.DeleteEnvoyRequest
	revR    []*pb.RevokeEnvoyRequest
}

func (c *recordingAdminClient) record(name string) {
	c.mu.Lock()
	c.calls = append(c.calls, name)
	c.mu.Unlock()
}

func (c *recordingAdminClient) GetEnvoy(_ context.Context, req *pb.GetEnvoyRequest, _ ...grpc.CallOption) (*pb.GetEnvoyResponse, error) {
	c.record("GetEnvoy")
	// The stored pubkey + cert pin ride the response, as on the real handler.
	return &pb.GetEnvoyResponse{Envoy: c.envoy, PubkeyPem: "pk-" + c.envoy.GetId(), CertSpkiSha256: testSPKI}, nil
}

// testSPKI is a well-formed cert pin (64 lowercase hex) the fake GetEnvoy returns.
const testSPKI = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func (c *recordingAdminClient) EnrollEnvoy(_ context.Context, req *pb.EnrollEnvoyRequest, _ ...grpc.CallOption) (*pb.EnrollEnvoyResponse, error) {
	c.record("EnrollEnvoy")
	// Mirror the real handler's contract (server/grpc/enroll.go): no pubkey,
	// no enrollment. Without this the fake accepted what production rejects.
	if req.GetPubkeyPem() == "" {
		return nil, fmt.Errorf("hostname and pubkey_pem are required")
	}
	c.mu.Lock()
	c.enrollR = append(c.enrollR, req)
	c.mu.Unlock()
	return &pb.EnrollEnvoyResponse{EnvoyId: c.newID}, nil
}

func (c *recordingAdminClient) SetReassignment(_ context.Context, req *pb.SetReassignmentRequest, _ ...grpc.CallOption) (*pb.SetReassignmentResponse, error) {
	c.record("SetReassignment")
	c.mu.Lock()
	c.reassR = append(c.reassR, req)
	c.mu.Unlock()
	return &pb.SetReassignmentResponse{}, nil
}

func (c *recordingAdminClient) ForcePush(_ context.Context, req *pb.ForcePushRequest, _ ...grpc.CallOption) (*pb.ForcePushResponse, error) {
	c.record("ForcePush")
	c.mu.Lock()
	c.fpR = append(c.fpR, req)
	c.mu.Unlock()
	return &pb.ForcePushResponse{EnvoysNotified: c.notify}, nil
}

func (c *recordingAdminClient) ListEnvoys(_ context.Context, _ *pb.ListEnvoysRequest, _ ...grpc.CallOption) (*pb.ListEnvoysResponse, error) {
	c.record("ListEnvoys")
	return &pb.ListEnvoysResponse{Envoys: c.envoys}, nil
}

func (c *recordingAdminClient) DeleteEnvoy(_ context.Context, req *pb.DeleteEnvoyRequest, _ ...grpc.CallOption) (*pb.DeleteEnvoyResponse, error) {
	c.record("DeleteEnvoy")
	c.mu.Lock()
	c.delR = append(c.delR, req)
	c.mu.Unlock()
	return &pb.DeleteEnvoyResponse{}, nil
}

func (c *recordingAdminClient) RevokeEnvoy(_ context.Context, req *pb.RevokeEnvoyRequest, _ ...grpc.CallOption) (*pb.RevokeEnvoyResponse, error) {
	c.record("RevokeEnvoy")
	c.mu.Lock()
	c.revR = append(c.revR, req)
	c.mu.Unlock()
	return &pb.RevokeEnvoyResponse{}, nil
}

// --- Step 3: force-push routing ---

// TestForcePushToBolt routes a ForcePush to the named bolt via the
// injected single-bolt dialer.
func TestForcePushToBolt(t *testing.T) {
	a := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")
	rec := &recordingAdminClient{notify: 7}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		if host != "owner.example.com" {
			t.Errorf("force-push dialed %q, want owner.example.com", host)
		}
		return &PeerConn{ID: host, Admin: rec}, nil
	}

	count, err := a.ForcePushToBolt(context.Background(), "owner.example.com", "envoy-1")
	if err != nil {
		t.Fatalf("ForcePushToBolt: %v", err)
	}
	if count != 7 {
		t.Errorf("envoys notified = %d, want 7", count)
	}
	if len(rec.fpR) != 1 || rec.fpR[0].EnvoyId != "envoy-1" {
		t.Errorf("ForcePush request = %+v, want EnvoyId envoy-1", rec.fpR)
	}
}

// TestDeleteEnvoyToBolt routes a DeleteEnvoy to the owning bolt via the
// injected single-bolt dialer, carrying the skip-swarm-cleanup flag.
func TestDeleteEnvoyToBolt(t *testing.T) {
	a := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")
	rec := &recordingAdminClient{}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		if host != "owner.example.com" {
			t.Errorf("delete dialed %q, want owner.example.com", host)
		}
		return &PeerConn{ID: host, Admin: rec}, nil
	}

	if err := a.DeleteEnvoyToBolt(context.Background(), "owner.example.com", "envoy-1", true); err != nil {
		t.Fatalf("DeleteEnvoyToBolt: %v", err)
	}
	if len(rec.delR) != 1 || rec.delR[0].Id != "envoy-1" || !rec.delR[0].SkipSwarmCleanup {
		t.Errorf("DeleteEnvoy request = %+v, want Id=envoy-1 skip=true", rec.delR)
	}
}

// TestRevokeEnvoyToBolt routes a RevokeEnvoy to the owning bolt.
func TestRevokeEnvoyToBolt(t *testing.T) {
	a := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")
	rec := &recordingAdminClient{}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		if host != "owner.example.com" {
			t.Errorf("revoke dialed %q, want owner.example.com", host)
		}
		return &PeerConn{ID: host, Admin: rec}, nil
	}

	if err := a.RevokeEnvoyToBolt(context.Background(), "owner.example.com", "envoy-1", false); err != nil {
		t.Fatalf("RevokeEnvoyToBolt: %v", err)
	}
	if len(rec.revR) != 1 || rec.revR[0].Id != "envoy-1" || rec.revR[0].SkipSwarmCleanup {
		t.Errorf("RevokeEnvoy request = %+v, want Id=envoy-1 skip=false", rec.revR)
	}
}

// --- Step 4: reassign + drain ---

// TestReassignEnvoyRPCOrdering asserts the three reassign RPCs fire
// in the documented order: source GetEnvoy → target EnrollEnvoy →
// source SetReassignment, with the target address threaded through.
func TestReassignEnvoyRPCOrdering(t *testing.T) {
	a := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")

	src := &recordingAdminClient{
		envoy: &pb.Envoy{Id: "envoy-1", Hostname: "node.example.com", IpAddress: "10.0.0.5"},
	}
	dst := &recordingAdminClient{newID: "bolt-assigned-id"}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		switch host {
		case "src.example.com":
			return &PeerConn{ID: host, Admin: src}, nil
		case "dst.example.com":
			return &PeerConn{ID: host, Admin: dst}, nil
		default:
			t.Fatalf("unexpected dial to %q", host)
			return nil, nil
		}
	}

	result, err := a.ReassignEnvoy(context.Background(), "envoy-1", "src.example.com", "dst.example.com")
	if err != nil {
		t.Fatalf("ReassignEnvoy: %v", err)
	}
	if result.NewEnvoyID != "bolt-assigned-id" {
		t.Errorf("new envoy id = %q, want bolt-assigned-id", result.NewEnvoyID)
	}
	if result.TargetAddr != "dst.example.com:1530" {
		t.Errorf("target addr = %q, want dst.example.com:1530", result.TargetAddr)
	}

	// Step ordering: source GetEnvoy first, then SetReassignment.
	if len(src.calls) != 2 || src.calls[0] != "GetEnvoy" || src.calls[1] != "SetReassignment" {
		t.Errorf("source RPC order = %v, want [GetEnvoy SetReassignment]", src.calls)
	}
	// Target gets exactly one EnrollEnvoy, between the two source calls.
	if len(dst.calls) != 1 || dst.calls[0] != "EnrollEnvoy" {
		t.Errorf("target RPC order = %v, want [EnrollEnvoy]", dst.calls)
	}
	// EnrollEnvoy carried the envoy's identity from GetEnvoy.
	if len(dst.enrollR) != 1 || dst.enrollR[0].Hostname != "node.example.com" || dst.enrollR[0].IpAddress != "10.0.0.5" {
		t.Errorf("EnrollEnvoy request = %+v, want hostname+ip from the source envoy", dst.enrollR)
	}
	if len(dst.enrollR) == 1 && (dst.enrollR[0].PubkeyPem != "pk-envoy-1" || dst.enrollR[0].CertSpkiSha256 != testSPKI) {
		t.Errorf("EnrollEnvoy pubkey/pin = %q/%q, want the source's stored pubkey + cert pin", dst.enrollR[0].PubkeyPem, dst.enrollR[0].CertSpkiSha256)
	}
	// SetReassignment pointed the agent at the target bolt + new id.
	if len(src.reassR) != 1 || src.reassR[0].TargetAddr != "dst.example.com:1530" || src.reassR[0].NewEnvoyId != "bolt-assigned-id" {
		t.Errorf("SetReassignment request = %+v, want target dst.example.com:1530 / new id bolt-assigned-id", src.reassR)
	}
}

// TestReassignEnvoyAutoTarget — with no explicit target the envoy is
// auto-routed by hostname through OwningBolt against the live roster.
func TestReassignEnvoyAutoTarget(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")
	insertRosterBolt(t, q, hexKey(0x33), "auto-target.example.com", 100, `["*.web.example.com"]`)

	src := &recordingAdminClient{
		envoy: &pb.Envoy{Id: "e1", Hostname: "n1.web.example.com", IpAddress: "10.0.0.9"},
	}
	dst := &recordingAdminClient{newID: "new-e1"}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		switch host {
		case "src.example.com":
			return &PeerConn{ID: host, Admin: src}, nil
		case "auto-target.example.com":
			return &PeerConn{ID: host, Admin: dst}, nil
		default:
			t.Fatalf("unexpected dial to %q", host)
			return nil, nil
		}
	}

	result, err := a.ReassignEnvoy(context.Background(), "e1", "src.example.com", "")
	if err != nil {
		t.Fatalf("ReassignEnvoy (auto-target): %v", err)
	}
	if result.TargetAddr != "auto-target.example.com:1530" {
		t.Errorf("auto-routed target = %q, want auto-target.example.com:1530", result.TargetAddr)
	}
}

// TestDrainBoltBatch drains every non-revoked envoy off a source bolt
// onto an explicit target, skipping the revoked one.
func TestDrainBoltBatch(t *testing.T) {
	a := NewSpannerAdmin(nil, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")

	src := &recordingAdminClient{
		envoys: []*pb.Envoy{
			{Id: "e1", Hostname: "h1"},
			{Id: "e2", Hostname: "h2"},
			{Id: "e3", Hostname: "h3", Revoked: true}, // skipped
		},
		envoy: &pb.Envoy{Id: "x", Hostname: "h", IpAddress: "10.0.0.1"},
	}
	dst := &recordingAdminClient{newID: "moved"}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		switch host {
		case "drain-src.example.com":
			return &PeerConn{ID: host, Admin: src}, nil
		case "drain-dst.example.com":
			return &PeerConn{ID: host, Admin: dst}, nil
		default:
			t.Fatalf("unexpected dial to %q", host)
			return nil, nil
		}
	}

	result, err := a.DrainBolt(context.Background(), "drain-src.example.com", "drain-dst.example.com")
	if err != nil {
		t.Fatalf("DrainBolt: %v", err)
	}
	if result.Moved != 2 {
		t.Errorf("drained = %d, want 2 (revoked envoy skipped)", result.Moved)
	}
	if result.Failed != 0 {
		t.Errorf("failed = %d, want 0; errors=%v", result.Failed, result.Errors)
	}
	// Two reassignments → two EnrollEnvoy on the target.
	if len(dst.enrollR) != 2 {
		t.Errorf("target EnrollEnvoy count = %d, want 2", len(dst.enrollR))
	}
}

// TestDrainBoltAutoRouteExcludesSource — draining with no explicit
// target must auto-route each envoy to a bolt OTHER than the one being
// drained, even when the drained bolt's own pattern claims still match
// the envoy's hostname. An envoy no other bolt claims is a drain
// failure, not a fake success.
func TestDrainBoltAutoRouteExcludesSource(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, "self", FallbackAdminLocal, "1530")

	// The source bolt (oldest-admitted) claims "*" — without the
	// exclusion it would win the auto-route for every hostname. A
	// second bolt claims only "*.west.example.com".
	insertRosterBolt(t, q, hexKey(0x41), "drain-src.example.com", 100, `["*"]`)
	insertRosterBolt(t, q, hexKey(0x42), "west-bolt.example.com", 200, `["*.west.example.com"]`)

	src := &recordingAdminClient{
		envoys: []*pb.Envoy{
			{Id: "e-west", Hostname: "n1.west.example.com"},
			{Id: "e-orphan", Hostname: "n1.other.example.com"}, // only "*" (the source) claims it
		},
		envoy: &pb.Envoy{Id: "e-west", Hostname: "n1.west.example.com", IpAddress: "10.0.0.1"},
	}
	dst := &recordingAdminClient{newID: "moved"}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		switch host {
		case "drain-src.example.com":
			return &PeerConn{ID: host, Admin: src}, nil
		case "west-bolt.example.com":
			return &PeerConn{ID: host, Admin: dst}, nil
		default:
			t.Fatalf("drain auto-route dialed %q — it must never target the source bolt", host)
			return nil, nil
		}
	}

	result, err := a.DrainBolt(context.Background(), "drain-src.example.com", "")
	if err != nil {
		t.Fatalf("DrainBolt auto-route: %v", err)
	}
	if result.Moved != 1 {
		t.Errorf("moved = %d, want 1 (e-west → west-bolt)", result.Moved)
	}
	if result.Failed != 1 {
		t.Errorf("failed = %d, want 1 (e-orphan has no non-source claimant); errors=%v", result.Failed, result.Errors)
	}
}

// --- multi-bolt integration: two bolts, no hub ---

// TestPeerEqualMultiBoltRouting stands up a SpannerAdmin over a
// two-bolt roster with no hub and walks the full Slice 2-ii-b path:
// OwningBolt resolves the enrollment redirect target, and a
// reassignment moves an envoy between the two bolts via direct peer
// RPCs only.
func TestPeerEqualMultiBoltRouting(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)

	// Local bolt is "self"; peerHex is the other bolt in the spanner.
	selfHex := hexKey(0x10)
	peerHex := hexKey(0x20)
	a := NewSpannerAdmin(q, NewObservabilityCache(), nil, selfHex, FallbackAdminLocal, "1530")

	insertRosterBolt(t, q, selfHex, "self.example.com", 100, `["*.east.example.com"]`)
	insertRosterBolt(t, q, peerHex, "peer.example.com", 200, `["*.west.example.com"]`)

	// Enrollment routing: an east host stays local; a west host
	// redirects to the peer bolt.
	if pk, _, owned := a.OwningBolt(context.Background(), "n1.east.example.com"); !owned || !a.OwnsLocally(pk) {
		t.Errorf("east host should be owned locally; pk=%q owned=%v", pk, owned)
	}
	pk, peerHost, owned := a.OwningBolt(context.Background(), "n1.west.example.com")
	if !owned {
		t.Fatal("west host should resolve to an owning bolt")
	}
	if a.OwnsLocally(pk) {
		t.Error("west host should NOT be owned locally — redirect expected")
	}
	if peerHost != "peer.example.com" {
		t.Errorf("redirect target hostname = %q, want peer.example.com", peerHost)
	}

	// Reassignment between the two bolts via direct peer RPCs only.
	self := &recordingAdminClient{
		envoy: &pb.Envoy{Id: "e-east-1", Hostname: "n1.east.example.com", IpAddress: "10.1.0.1"},
	}
	peer := &recordingAdminClient{newID: "e-west-1"}
	a.dialBoltFn = func(host string) (*PeerConn, error) {
		switch host {
		case "self.example.com":
			return &PeerConn{ID: host, Admin: self}, nil
		case "peer.example.com":
			return &PeerConn{ID: host, Admin: peer}, nil
		default:
			t.Fatalf("unexpected dial to %q", host)
			return nil, nil
		}
	}
	result, err := a.ReassignEnvoy(context.Background(), "e-east-1", "self.example.com", "peer.example.com")
	if err != nil {
		t.Fatalf("ReassignEnvoy across bolts: %v", err)
	}
	if result.TargetAddr != "peer.example.com:1530" || result.NewEnvoyID != "e-west-1" {
		t.Errorf("cross-bolt reassign result = %+v", result)
	}
}
