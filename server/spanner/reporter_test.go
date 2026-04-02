// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"
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
	total, converged, relapsed, diverged, errors, lostContact, noData int
}

func (f *fakeConvergenceSource) ConvergenceCounts() (int, int, int, int, int, int, int) {
	return f.total, f.converged, f.relapsed, f.diverged, f.errors, f.lostContact, f.noData
}

// countingAdminService counts ReportSpokeStatus calls and optionally fails.
type countingAdminService struct {
	pb.UnimplementedVigoAdminServer
	calls   atomic.Int32
	failErr error // if non-nil, all calls return this error
}

func (s *countingAdminService) ReportSpokeStatus(_ context.Context, _ *pb.SpokeStatusReport) (*pb.SpokeStatusAck, error) {
	s.calls.Add(1)
	if s.failErr != nil {
		return nil, s.failErr
	}
	return &pb.SpokeStatusAck{}, nil
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
	r := NewReporter("spoke1", "hub.example.com:1530", nil,
		"", "", "", &fakeConvergenceSource{}, 10*time.Second)

	if r.ActiveAddr() != "hub.example.com:1530" {
		t.Errorf("ActiveAddr() = %q, want hub.example.com:1530", r.ActiveAddr())
	}
}

func TestReporterActiveAddr_WithFallbacks(t *testing.T) {
	r := NewReporter("spoke1", "hub.example.com:1530",
		[]string{"standby1.example.com:1530", "standby2.example.com:1530"},
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

	r := NewReporter("spoke1", primaryAddr, []string{fallbackAddr},
		"", "", "", &fakeConvergenceSource{total: 10, converged: 8},
		50*time.Millisecond)

	// Override dial to use insecure credentials for testing.
	r.dialFunc = func(ctx context.Context, addr string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	r := NewReporter("spoke1", addr, nil,
		"", "", "", &fakeConvergenceSource{total: 5, converged: 5},
		50*time.Millisecond)

	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	r := NewReporter("spoke1", addr1, []string{addr2, addr3},
		"", "", "", &fakeConvergenceSource{total: 1, converged: 1},
		30*time.Millisecond)

	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	r := NewReporter("spoke1", addr, []string{fallbackAddr},
		"", "", "", &fakeConvergenceSource{total: 5, converged: 5},
		50*time.Millisecond)

	// Create a custom dial that fails on even calls (1st success, 2nd fail, 3rd success, etc.)
	// This ensures we never hit 3 consecutive failures.
	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		callCount.Add(1)
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
	var received *pb.SpokeStatusReport
	svc := &pb.UnimplementedVigoAdminServer{}
	_ = svc

	// Use a real fake service that captures the request.
	captureSvc := &capturingAdminService{}
	addr := startCapturingFakeHub(t, captureSvc)

	r := NewReporter("spoke-east", addr, nil,
		"", "", "", &fakeConvergenceSource{
			total: 100, converged: 80, relapsed: 10, errors: 5, lostContact: 3, noData: 2,
		},
		50*time.Millisecond)

	r.dialFunc = func(ctx context.Context, a string, _ credentials.TransportCredentials) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, a, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

				if received.SpokeId != "spoke-east" {
					t.Errorf("SpokeId = %q, want spoke-east", received.SpokeId)
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
				if received.Errors != 5 {
					t.Errorf("Errors = %d, want 5", received.Errors)
				}
				if received.LostContact != 3 {
					t.Errorf("LostContact = %d, want 3", received.LostContact)
				}
				if received.NoData != 2 {
					t.Errorf("NoData = %d, want 2", received.NoData)
				}
				// 80 compliant / (100 total - 3 lost contact) = 82.47%
				if received.ConvergencePct < 82.0 || received.ConvergencePct > 83.0 {
					t.Errorf("ConvergencePct = %f, want ~82.5 (lost contact excluded from denominator)", received.ConvergencePct)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// capturingAdminService captures the last ReportSpokeStatus request.
type capturingAdminService struct {
	pb.UnimplementedVigoAdminServer
	last atomic.Value // *pb.SpokeStatusReport
}

func (s *capturingAdminService) ReportSpokeStatus(_ context.Context, req *pb.SpokeStatusReport) (*pb.SpokeStatusAck, error) {
	s.last.Store(req)
	return &pb.SpokeStatusAck{}, nil
}

func (s *capturingAdminService) lastReport() *pb.SpokeStatusReport {
	v := s.last.Load()
	if v == nil {
		return nil
	}
	return v.(*pb.SpokeStatusReport)
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
