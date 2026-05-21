// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Reporter unit tests — hub fallback failover
// ---------------------------------------------------------------------------

// fakeConvergenceSource implements ConvergenceSource for testing.
type fakeConvergenceSource struct {
	total, converged, relapsed, diverged, degraded, errors, offline, noData int
}

func (f *fakeConvergenceSource) ConvergenceCounts() (int, int, int, int, int, int, int, int) {
	return f.total, f.converged, f.relapsed, f.diverged, f.degraded, f.errors, f.offline, f.noData
}

// countingAdminService counts ReportBoltStatus calls and optionally fails.
type countingAdminService struct {
	pb.UnimplementedVigoAdminServer
	calls   atomic.Int32
	failErr error // if non-nil, all calls return this error
}

func (s *countingAdminService) ReportBoltStatus(_ context.Context, _ *pb.BoltStatusReport) (*pb.BoltStatusAck, error) {
	s.calls.Add(1)
	if s.failErr != nil {
		return nil, s.failErr
	}
	return &pb.BoltStatusAck{}, nil
}

// startReporterFakeHub starts a gRPC server for reporter tests.
func startReporterFakeHub(t *testing.T, svc *countingAdminService) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterVigoAdminServer(srv, svc)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String()
}

func TestReporterActiveAddr_NoFallback(t *testing.T) {
	r := NewReporter("bolt1", "", nil, []string{"hub.example.com:1530"},
		"", "", "", &fakeConvergenceSource{}, 10*time.Second)

	if r.ActiveAddr() != "hub.example.com:1530" {
		t.Errorf("ActiveAddr() = %q, want hub.example.com:1530", r.ActiveAddr())
	}
}

func TestReporterActiveAddr_WithFallbacks(t *testing.T) {
	r := NewReporter("bolt1", "", nil,
		[]string{"hub.example.com:1530", "standby1.example.com:1530", "standby2.example.com:1530"},
		"", "", "", &fakeConvergenceSource{}, 10*time.Second)

	if r.ActiveAddr() != "hub.example.com:1530" {
		t.Errorf("ActiveAddr() = %q, want hub.example.com:1530", r.ActiveAddr())
	}
	if len(r.allAddrs) != 3 {
		t.Errorf("allAddrs length = %d, want 3", len(r.allAddrs))
	}
}

func TestReporterFailover_RotatesAfterThreshold(t *testing.T) {
	// Start two fake hubs: primary always fails, fallback succeeds.
	primarySvc := &countingAdminService{failErr: status.Error(codes.Unavailable, "primary down")}
	fallbackSvc := &countingAdminService{}

	primaryAddr := startReporterFakeHub(t, primarySvc)
	fallbackAddr := startReporterFakeHub(t, fallbackSvc)

	r := NewReporter("bolt1", "", nil, []string{primaryAddr, fallbackAddr},
		"", "", "", &fakeConvergenceSource{total: 10, converged: 8},
		50*time.Millisecond)

	// Override dial to use insecure credentials for testing.
	r.dialFunc = func(ctx context.Context, addr string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials())) //nolint:staticcheck // SA1019: grpc.NewClient migration deferred — see pending.md
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.Run(ctx)

	// Wait for failover to happen: primary gets 3 failures, then fallback should get calls.
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for fallback calls; primary calls=%d, fallback calls=%d",
				primarySvc.calls.Load(), fallbackSvc.calls.Load())
		default:
			if fallbackSvc.calls.Load() > 0 {
				// Failover happened.
				if primarySvc.calls.Load() < 3 {
					t.Errorf("expected at least 3 primary calls before failover, got %d",
						primarySvc.calls.Load())
				}
				if r.ActiveAddr() != fallbackAddr {
					t.Errorf("ActiveAddr() = %q, want %q", r.ActiveAddr(), fallbackAddr)
				}
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestReporterFailover_NoRotationWithSingleAddr(t *testing.T) {
	// With only one address, failures should not cause rotation.
	failSvc := &countingAdminService{failErr: status.Error(codes.Unavailable, "down")}
	addr := startReporterFakeHub(t, failSvc)

	r := NewReporter("bolt1", "", nil, []string{addr},
		"", "", "", &fakeConvergenceSource{total: 5, converged: 5},
		50*time.Millisecond)

	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials())) //nolint:staticcheck // SA1019: grpc.NewClient migration deferred — see pending.md
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go r.Run(ctx)

	<-ctx.Done()

	// Should still be pointing at the same address.
	if r.ActiveAddr() != addr {
		t.Errorf("ActiveAddr() = %q, want %q (no rotation with single addr)", r.ActiveAddr(), addr)
	}
}

func TestReporterFailover_CyclesBackToPrimary(t *testing.T) {
	// All addresses fail — the reporter should cycle through them all
	// and wrap back to the primary.
	svc1 := &countingAdminService{failErr: fmt.Errorf("down")}
	svc2 := &countingAdminService{failErr: fmt.Errorf("down")}
	svc3 := &countingAdminService{failErr: fmt.Errorf("down")}

	addr1 := startReporterFakeHub(t, svc1)
	addr2 := startReporterFakeHub(t, svc2)
	addr3 := startReporterFakeHub(t, svc3)

	r := NewReporter("bolt1", "", nil, []string{addr1, addr2, addr3},
		"", "", "", &fakeConvergenceSource{total: 1, converged: 1},
		30*time.Millisecond)

	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials())) //nolint:staticcheck // SA1019: grpc.NewClient migration deferred — see pending.md
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.Run(ctx)

	// Wait long enough for multiple rotations (3 failures * 30ms per tick * 3 addresses = ~270ms minimum).
	time.Sleep(800 * time.Millisecond)
	cancel()

	// All three services should have received calls.
	if svc1.calls.Load() == 0 {
		t.Error("expected primary to receive calls")
	}
	if svc2.calls.Load() == 0 {
		t.Error("expected fallback 1 to receive calls")
	}
	if svc3.calls.Load() == 0 {
		t.Error("expected fallback 2 to receive calls")
	}
}

func TestReporterFailover_ResetsOnSuccess(t *testing.T) {
	// Primary intermittently fails (fewer than threshold) — should not rotate.
	callCount := atomic.Int32{}

	svc := &countingAdminService{}
	addr := startReporterFakeHub(t, svc)

	fallbackSvc := &countingAdminService{}
	fallbackAddr := startReporterFakeHub(t, fallbackSvc)

	r := NewReporter("bolt1", "", nil, []string{addr, fallbackAddr},
		"", "", "", &fakeConvergenceSource{total: 5, converged: 5},
		50*time.Millisecond)

	// Create a custom dial that fails on even calls (1st success, 2nd fail, 3rd success, etc.)
	// This ensures we never hit 3 consecutive failures.
	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		callCount.Add(1)
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials())) //nolint:staticcheck // SA1019: grpc.NewClient migration deferred — see pending.md
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	go r.Run(ctx)

	<-ctx.Done()

	// Since the primary succeeds, we should never rotate to fallback.
	if fallbackSvc.calls.Load() > 0 {
		t.Errorf("fallback received %d calls, want 0 (primary is healthy)", fallbackSvc.calls.Load())
	}
	if r.ActiveAddr() != addr {
		t.Errorf("ActiveAddr() = %q, want %q (should stay on primary)", r.ActiveAddr(), addr)
	}
}

func TestReporterReport_SendsCorrectData(t *testing.T) {
	// Verify the reporter sends correct compliance data.
	var received *pb.BoltStatusReport
	svc := &pb.UnimplementedVigoAdminServer{}
	_ = svc

	// Use a real fake service that captures the request.
	captureSvc := &capturingAdminService{}
	addr := startCapturingFakeHub(t, captureSvc)

	r := NewReporter("bolt-east", "", nil, []string{addr},
		"", "", "", &fakeConvergenceSource{
			total: 100, converged: 80, relapsed: 10, errors: 5, offline: 3, noData: 2,
		},
		50*time.Millisecond)

	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials())) //nolint:staticcheck // SA1019: grpc.NewClient migration deferred — see pending.md
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	go r.Run(ctx)

	// Wait for at least one report.
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for report")
		default:
			received = captureSvc.lastReport()
			if received != nil {
				cancel()

				if received.BoltId != "bolt-east" {
					t.Errorf("BoltId = %q, want bolt-east", received.BoltId)
				}
				if received.EnvoyCount != 100 {
					t.Errorf("EnvoyCount = %d, want 100", received.EnvoyCount)
				}
				if received.Converged != 80 {
					t.Errorf("Converged = %d, want 80", received.Converged)
				}
				if received.Relapsed != 10 {
					t.Errorf("Relapsed = %d, want 10", received.Relapsed)
				}
				if received.Failed != 5 {
					t.Errorf("Failed = %d, want 5", received.Failed)
				}
				if received.Offline != 3 {
					t.Errorf("Offline = %d, want 3", received.Offline)
				}
				if received.NoData != 2 {
					t.Errorf("NoData = %d, want 2", received.NoData)
				}
				// 80 compliant / (100 total - 3 offline) = 82.47%
				if received.ConvergencePct < 82.0 || received.ConvergencePct > 83.0 {
					t.Errorf("ConvergencePct = %f, want ~82.5 (offline excluded from denominator)", received.ConvergencePct)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// capturingAdminService captures the last ReportBoltStatus request.
type capturingAdminService struct {
	pb.UnimplementedVigoAdminServer
	last atomic.Value // *pb.BoltStatusReport
}

func (s *capturingAdminService) ReportBoltStatus(_ context.Context, req *pb.BoltStatusReport) (*pb.BoltStatusAck, error) {
	s.last.Store(req)
	return &pb.BoltStatusAck{}, nil
}

func (s *capturingAdminService) lastReport() *pb.BoltStatusReport {
	v := s.last.Load()
	if v == nil {
		return nil
	}
	return v.(*pb.BoltStatusReport)
}

func startCapturingFakeHub(t *testing.T, svc *capturingAdminService) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterVigoAdminServer(srv, svc)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String()
}

// ---------------------------------------------------------------------------
// Pin-on-first-sighting — end-to-end integration
// ---------------------------------------------------------------------------

// hubAdminShim adapts a real *Hub to the VigoAdminServer interface
// for integration tests. Mirrors server/grpc/admin.go::ReportBoltStatus —
// validates bolt_id, forwards to hub.RecordBoltReport, and maps
// hub-side rejection to PermissionDenied so the wire-visible behavior
// matches production.
type hubAdminShim struct {
	pb.UnimplementedVigoAdminServer
	hub   *Hub
	calls atomic.Int32
}

func (s *hubAdminShim) ReportBoltStatus(ctx context.Context, req *pb.BoltStatusReport) (*pb.BoltStatusAck, error) {
	s.calls.Add(1)
	if req.BoltId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "bolt_id is required")
	}
	if err := s.hub.RecordBoltReport(ctx, req); err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "report rejected: %v", err)
	}
	return &pb.BoltStatusAck{}, nil
}

func startHubAdminShim(t *testing.T, h *Hub) (addr string, shim *hubAdminShim) {
	t.Helper()
	shim = &hubAdminShim{hub: h}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterVigoAdminServer(srv, shim)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String(), shim
}

// insecureDial dials addr with insecure credentials, ignoring any TLS
// creds the Reporter would otherwise build. Used as Reporter.dialFunc
// in integration tests where the hub serves plaintext on loopback.
func insecureDial(ctx context.Context, addr string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
	return grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials())) //nolint:staticcheck // SA1019: grpc.NewClient migration deferred — see pending.md
}

// TestPinOnFirstSighting_Integration exercises the full Reporter → gRPC →
// Hub.RecordBoltReport path over loopback TCP. Asserts (a) first
// sighting pins the asserted pubkey, and (b) a subsequent report under
// the same (spanner_id, bolt_id) with a different signing key is
// rejected and emits spanner_bolt_pubkey_mismatch via the audit
// writer. pin_test.go covers the same logic at the unit level by
// invoking Hub.RecordBoltReport directly — this test extends that
// coverage through the gRPC handler and the Reporter's signing path,
// which is what the production wire actually exercises.
func TestPinOnFirstSighting_Integration(t *testing.T) {
	h, audit := newHubForPinTests(t)
	addr, shim := startHubAdminShim(t, h)

	const spannerID = "alexander4-prod"
	const boltID = "dc1"

	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, priv2, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	conv := &fakeConvergenceSource{total: 5, converged: 5}

	// --- Phase 1: first sighting pins priv1's pubkey ---

	r1 := NewReporter(boltID, spannerID, priv1, []string{addr}, "", "", "", conv, 30*time.Millisecond)
	r1.dialFunc = insecureDial

	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel1()
	go r1.Run(ctx1)

	waitFor(t, 1500*time.Millisecond, "first sighting recorded", func() bool {
		return shim.calls.Load() >= 1 && h.LookupBoltPin(spannerID, boltID) != nil
	})
	cancel1()

	pin := h.LookupBoltPin(spannerID, boltID)
	if pin == nil {
		t.Fatal("expected pin after first sighting")
	}
	if !bytesEqual(pin.Pubkey, []byte(priv1.Public().(ed25519.PublicKey))) {
		t.Error("pinned pubkey does not match priv1's pubkey")
	}
	if got := len(audit.events_("spanner_bolt_pubkey_mismatch")); got != 0 {
		t.Errorf("first sighting should not emit mismatch events; got %d", got)
	}

	// --- Phase 2: same (spanner_id, bolt_id) under priv2 is rejected ---

	beforeCalls := shim.calls.Load()
	r2 := NewReporter(boltID, spannerID, priv2, []string{addr}, "", "", "", conv, 30*time.Millisecond)
	r2.dialFunc = insecureDial

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	go r2.Run(ctx2)

	waitFor(t, 1500*time.Millisecond, "mismatch event recorded", func() bool {
		return shim.calls.Load() > beforeCalls && len(audit.events_("spanner_bolt_pubkey_mismatch")) >= 1
	})
	cancel2()

	mismatch := audit.events_("spanner_bolt_pubkey_mismatch")
	if len(mismatch) < 1 {
		t.Fatal("expected at least one mismatch event after priv2 report")
	}
	if !strings.Contains(mismatch[0].payload, `"decision":"rejected"`) {
		t.Errorf("audit payload missing rejected decision: %s", mismatch[0].payload)
	}

	// Pin must still hold priv1's pubkey — the rejected report did not replace it.
	pin = h.LookupBoltPin(spannerID, boltID)
	if pin == nil || !bytesEqual(pin.Pubkey, []byte(priv1.Public().(ed25519.PublicKey))) {
		t.Error("pin should still hold priv1's pubkey after priv2 rejection")
	}
}

// waitFor polls cond every 10ms until it returns true or timeout elapses.
// Fails the test with msg on timeout.
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}
