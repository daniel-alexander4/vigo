// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tungol

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// fakeAdminService: real gRPC server for integration-style fan-out tests
// ---------------------------------------------------------------------------

// fakeAdminService implements VigoAdminServer for testing hub fan-out.
type fakeAdminService struct {
	pb.UnimplementedVigoAdminServer
	envoys    []*pb.Envoy
	runs       []*pb.Run
	convergence *pb.GetConvergenceSummaryResponse
	forcePush  int32
}

func (f *fakeAdminService) ListEnvoys(_ context.Context, _ *pb.ListEnvoysRequest) (*pb.ListEnvoysResponse, error) {
	return &pb.ListEnvoysResponse{Envoys: f.envoys}, nil
}

func (f *fakeAdminService) ListRuns(_ context.Context, _ *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	return &pb.ListRunsResponse{Runs: f.runs}, nil
}

func (f *fakeAdminService) GetConvergenceSummary(_ context.Context, _ *pb.GetConvergenceSummaryRequest) (*pb.GetConvergenceSummaryResponse, error) {
	return f.convergence, nil
}

func (f *fakeAdminService) ForcePush(_ context.Context, _ *pb.ForcePushRequest) (*pb.ForcePushResponse, error) {
	return &pb.ForcePushResponse{EnvoysNotified: f.forcePush}, nil
}

// startFakeSteorra starts a gRPC server with a fake admin service and returns
// its address and a cleanup function.
func startFakeSteorra(t *testing.T, svc *fakeAdminService) string {
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

// newGRPCTestHub creates a Hub connected to test steorras via insecure transport.
func newGRPCTestHub(t *testing.T, addrs []string) *Hub {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h := &Hub{steorras: make([]steorraConn, 0, len(addrs))}
	for i, addr := range addrs {
		conn, err := grpc.DialContext(ctx, addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		if err != nil {
			t.Fatalf("connecting to fake steorra %d: %v", i, err)
		}
		t.Cleanup(func() { conn.Close() })
		h.steorras = append(h.steorras, steorraConn{
			id:    fmt.Sprintf("steorra%c", 'A'+i),
			addr:  addr,
			conn:  conn,
			admin: pb.NewVigoAdminClient(conn),
		})
	}
	return h
}

// ---------------------------------------------------------------------------
// mockAdminClient: in-process mock for unit tests (no real gRPC server)
// ---------------------------------------------------------------------------

// mockAdminClient implements pb.VigoAdminClient with canned responses.
type mockAdminClient struct {
	convergenceResp  *pb.GetConvergenceSummaryResponse
	convergenceErr   error
	listEnvoysResp *pb.ListEnvoysResponse
	listEnvoysErr  error
	listRunsResp    *pb.ListRunsResponse
	listRunsErr     error
	forcePushResp   *pb.ForcePushResponse
	forcePushErr    error
	getEnvoyResp   *pb.GetEnvoyResponse
	getEnvoyErr    error
	enrollResp      *pb.EnrollEnvoyResponse
	enrollErr       error
	setReassignResp *pb.SetReassignmentResponse
	setReassignErr  error

	// Track calls for assertions.
	enrollCalls      []*pb.EnrollEnvoyRequest
	setReassignCalls []*pb.SetReassignmentRequest
}

func (m *mockAdminClient) GetConvergenceSummary(_ context.Context, _ *pb.GetConvergenceSummaryRequest, _ ...grpc.CallOption) (*pb.GetConvergenceSummaryResponse, error) {
	return m.convergenceResp, m.convergenceErr
}

func (m *mockAdminClient) ListEnvoys(_ context.Context, _ *pb.ListEnvoysRequest, _ ...grpc.CallOption) (*pb.ListEnvoysResponse, error) {
	return m.listEnvoysResp, m.listEnvoysErr
}

func (m *mockAdminClient) ListRuns(_ context.Context, _ *pb.ListRunsRequest, _ ...grpc.CallOption) (*pb.ListRunsResponse, error) {
	return m.listRunsResp, m.listRunsErr
}

func (m *mockAdminClient) ForcePush(_ context.Context, _ *pb.ForcePushRequest, _ ...grpc.CallOption) (*pb.ForcePushResponse, error) {
	return m.forcePushResp, m.forcePushErr
}

func (m *mockAdminClient) GetEnvoy(_ context.Context, _ *pb.GetEnvoyRequest, _ ...grpc.CallOption) (*pb.GetEnvoyResponse, error) {
	return m.getEnvoyResp, m.getEnvoyErr
}

func (m *mockAdminClient) EnrollEnvoy(_ context.Context, req *pb.EnrollEnvoyRequest, _ ...grpc.CallOption) (*pb.EnrollEnvoyResponse, error) {
	m.enrollCalls = append(m.enrollCalls, req)
	return m.enrollResp, m.enrollErr
}

func (m *mockAdminClient) SetReassignment(_ context.Context, req *pb.SetReassignmentRequest, _ ...grpc.CallOption) (*pb.SetReassignmentResponse, error) {
	m.setReassignCalls = append(m.setReassignCalls, req)
	return m.setReassignResp, m.setReassignErr
}

// Unimplemented methods — return gRPC Unimplemented errors.

func (m *mockAdminClient) AddApprovedPattern(context.Context, *pb.AddApprovedPatternRequest, ...grpc.CallOption) (*pb.AddApprovedPatternResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) ListApprovedPatterns(context.Context, *pb.ListApprovedPatternsRequest, ...grpc.CallOption) (*pb.ListApprovedPatternsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) RevokeApprovedPattern(context.Context, *pb.RevokeApprovedPatternRequest, ...grpc.CallOption) (*pb.RevokeApprovedPatternResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) RevokeEnvoy(context.Context, *pb.RevokeEnvoyRequest, ...grpc.CallOption) (*pb.RevokeEnvoyResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) DeleteEnvoy(context.Context, *pb.DeleteEnvoyRequest, ...grpc.CallOption) (*pb.DeleteEnvoyResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) DryRun(context.Context, *pb.DryRunRequest, ...grpc.CallOption) (*pb.DryRunResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) GetRun(context.Context, *pb.GetRunRequest, ...grpc.CallOption) (*pb.GetRunResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) GenerateToken(context.Context, *pb.GenerateTokenRequest, ...grpc.CallOption) (*pb.GenerateTokenResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) AddWebhook(context.Context, *pb.AddWebhookRequest, ...grpc.CallOption) (*pb.AddWebhookResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) ListWebhooks(context.Context, *pb.ListWebhooksRequest, ...grpc.CallOption) (*pb.ListWebhooksResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) DeleteWebhook(context.Context, *pb.DeleteWebhookRequest, ...grpc.CallOption) (*pb.DeleteWebhookResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) GetEnvoyConvergence(context.Context, *pb.GetEnvoyConvergenceRequest, ...grpc.CallOption) (*pb.GetEnvoyConvergenceResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) Rollback(context.Context, *pb.RollbackRequest, ...grpc.CallOption) (*pb.RollbackResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) ListPackages(context.Context, *pb.ListPackagesRequest, ...grpc.CallOption) (*pb.ListPackagesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) ListPackageDrift(context.Context, *pb.ListPackageDriftRequest, ...grpc.CallOption) (*pb.ListPackageDriftResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) ReportSteorraStatus(context.Context, *pb.SteorraStatusReport, ...grpc.CallOption) (*pb.SteorraStatusAck, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
// newTestHub builds a Hub with mock steorra connections (no real gRPC).
func newTestHub(steorras ...steorraConn) *Hub {
	return &Hub{steorras: steorras}
}

// ---------------------------------------------------------------------------
// Fan-out integration tests (existing, using real gRPC)
// ---------------------------------------------------------------------------

func TestHub_AggregateListEnvoys(t *testing.T) {
	now := timestamppb.Now()

	steorraA := &fakeAdminService{
		envoys: []*pb.Envoy{
			{Id: "n1", Hostname: "web1.dc1", EnrolledAt: now},
			{Id: "n2", Hostname: "web2.dc1", EnrolledAt: now},
		},
	}
	steorraB := &fakeAdminService{
		envoys: []*pb.Envoy{
			{Id: "n3", Hostname: "web1.dc2", EnrolledAt: now},
		},
	}

	addrA := startFakeSteorra(t, steorraA)
	addrB := startFakeSteorra(t, steorraB)
	hub := newGRPCTestHub(t, []string{addrA, addrB})

	envoys, err := hub.AggregateListEnvoys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(envoys) != 3 {
		t.Fatalf("got %d envoys, want 3", len(envoys))
	}

	ids := map[string]bool{}
	for _, n := range envoys {
		ids[n.Id] = true
	}
	for _, want := range []string{"n1", "n2", "n3"} {
		if !ids[want] {
			t.Errorf("missing envoy %s", want)
		}
	}
}

func TestHub_AggregateConvergenceSummary(t *testing.T) {
	steorraA := &fakeAdminService{
		convergence: &pb.GetConvergenceSummaryResponse{
			Total: 10, Converged: 8, Relapsed: 1, Failed: 1,
		},
	}
	steorraB := &fakeAdminService{
		convergence: &pb.GetConvergenceSummaryResponse{
			Total: 5, Converged: 4, Offline: 1,
		},
	}

	addrA := startFakeSteorra(t, steorraA)
	addrB := startFakeSteorra(t, steorraB)
	hub := newGRPCTestHub(t, []string{addrA, addrB})

	resp, err := hub.AggregateConvergenceSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Total != 15 {
		t.Errorf("total = %d, want 15", resp.Total)
	}
	if resp.Converged != 12 {
		t.Errorf("compliant = %d, want 12", resp.Converged)
	}
	if resp.Relapsed != 1 {
		t.Errorf("relapsed = %d, want 1", resp.Relapsed)
	}
	if resp.Failed != 1 {
		t.Errorf("failed = %d, want 1", resp.Failed)
	}
	if resp.Offline != 1 {
		t.Errorf("offline = %d, want 1", resp.Offline)
	}
	// 12/(15-1 offline) = 85.7%
	if resp.ConvergencePct < 85.0 || resp.ConvergencePct > 86.5 {
		t.Errorf("compliance_pct = %f, want ~85.7 (offline excluded)", resp.ConvergencePct)
	}
}

func TestHub_AggregateListRuns(t *testing.T) {
	now := timestamppb.Now()
	steorraA := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r1", EnvoyId: "n1", StartedAt: now, Status: "success"},
		},
	}
	steorraB := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r2", EnvoyId: "n3", StartedAt: now, Status: "failure"},
			{Id: "r3", EnvoyId: "n3", StartedAt: now, Status: "success"},
		},
	}

	addrA := startFakeSteorra(t, steorraA)
	addrB := startFakeSteorra(t, steorraB)
	hub := newGRPCTestHub(t, []string{addrA, addrB})

	runs, err := hub.AggregateListRuns(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(runs))
	}
}

func TestHub_ForwardForcePush(t *testing.T) {
	steorraA := &fakeAdminService{forcePush: 5}
	steorraB := &fakeAdminService{forcePush: 3}

	addrA := startFakeSteorra(t, steorraA)
	addrB := startFakeSteorra(t, steorraB)
	hub := newGRPCTestHub(t, []string{addrA, addrB})

	total, err := hub.ForwardForcePush(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 8 {
		t.Errorf("total = %d, want 8", total)
	}
}

func TestHub_SteorraCount(t *testing.T) {
	hub := &Hub{steorras: make([]steorraConn, 3)}
	if hub.SteorraCount() != 3 {
		t.Errorf("SteorraCount() = %d, want 3", hub.SteorraCount())
	}
}

// ---------------------------------------------------------------------------
// Enriched steorra status tests (mock client, no real gRPC)
// ---------------------------------------------------------------------------

func TestSteorraStatusCompliance(t *testing.T) {
	mock := &mockAdminClient{
		convergenceResp: &pb.GetConvergenceSummaryResponse{
			Total:          20,
			Converged:      15,
			Relapsed: 2,
			Failed:         1,
			Offline:    1,
			NoData:         1,
			ConvergencePct:  75.0,
		},
	}

	hub := newTestHub(steorraConn{
		id:       "steorra-east",
		addr:     "east.example.com:1530",
		patterns: []string{"*.east.example.com"},
		admin:    mock,
	})

	statuses := hub.SteorraStatuses(context.Background())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	s := statuses[0]
	if s.ID != "steorra-east" {
		t.Errorf("ID = %q, want steorra-east", s.ID)
	}
	if s.Addr != "east.example.com:1530" {
		t.Errorf("Addr = %q, want east.example.com:1530", s.Addr)
	}
	if !s.Healthy {
		t.Error("expected steorra to be healthy")
	}
	if s.EnvoyCount != 20 {
		t.Errorf("EnvoyCount = %d, want 20", s.EnvoyCount)
	}
	if s.Converged != 15 {
		t.Errorf("Converged = %d, want 15", s.Converged)
	}
	if s.Relapsed != 2 {
		t.Errorf("Relapsed = %d, want 2", s.Relapsed)
	}
	if s.Failed != 1 {
		t.Errorf("Failed = %d, want 1", s.Failed)
	}
	if s.Offline != 1 {
		t.Errorf("Offline = %d, want 1", s.Offline)
	}
	if s.NoData != 1 {
		t.Errorf("NoData = %d, want 1", s.NoData)
	}
	if s.ConvergencePct != 75.0 {
		t.Errorf("ConvergencePct = %f, want 75.0", s.ConvergencePct)
	}
	if s.Error != "" {
		t.Errorf("Error = %q, want empty", s.Error)
	}
	if len(s.Patterns) != 1 || s.Patterns[0] != "*.east.example.com" {
		t.Errorf("Patterns = %v, want [*.east.example.com]", s.Patterns)
	}
}

func TestSteorraStatusLatency(t *testing.T) {
	mock := &mockAdminClient{
		convergenceResp: &pb.GetConvergenceSummaryResponse{Total: 5, Converged: 5},
	}

	hub := newTestHub(steorraConn{
		id:    "steorra-west",
		addr:  "west.example.com:1530",
		admin: mock,
	})

	statuses := hub.SteorraStatuses(context.Background())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	// Latency is non-negative (the mock returns instantly, so >= 0).
	if statuses[0].LatencyMs < 0 {
		t.Errorf("LatencyMs = %d, want >= 0", statuses[0].LatencyMs)
	}
	if !statuses[0].Healthy {
		t.Error("expected steorra to be healthy")
	}
}

func TestSteorraStatusError(t *testing.T) {
	mock := &mockAdminClient{
		convergenceErr: fmt.Errorf("connection refused"),
	}

	hub := newTestHub(steorraConn{
		id:    "steorra-down",
		addr:  "down:1530",
		admin: mock,
	})

	statuses := hub.SteorraStatuses(context.Background())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	s := statuses[0]
	if s.Healthy {
		t.Error("expected steorra to be unhealthy")
	}
	if s.Error == "" {
		t.Error("expected Error to be populated")
	}
	if s.EnvoyCount != 0 {
		t.Errorf("EnvoyCount = %d, want 0 on error", s.EnvoyCount)
	}
}

func TestMultipleSteorraStatuses(t *testing.T) {
	hub := newTestHub(
		steorraConn{id: "steorra-a", addr: "a:1530", admin: &mockAdminClient{
			convergenceResp: &pb.GetConvergenceSummaryResponse{Total: 10, Converged: 10, ConvergencePct: 100},
		}},
		steorraConn{id: "steorra-b", addr: "b:1530", admin: &mockAdminClient{
			convergenceResp: &pb.GetConvergenceSummaryResponse{Total: 5, Converged: 3, Relapsed: 2, ConvergencePct: 60},
		}},
	)

	statuses := hub.SteorraStatuses(context.Background())
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// Order matches steorra order.
	if statuses[0].ID != "steorra-a" || statuses[1].ID != "steorra-b" {
		t.Errorf("status IDs = [%s, %s], want [steorra-a, steorra-b]", statuses[0].ID, statuses[1].ID)
	}
	if statuses[0].EnvoyCount != 10 {
		t.Errorf("steorra-a EnvoyCount = %d, want 10", statuses[0].EnvoyCount)
	}
	if statuses[1].Relapsed != 2 {
		t.Errorf("steorra-b Relapsed = %d, want 2", statuses[1].Relapsed)
	}
}

// ---------------------------------------------------------------------------
// SteorraByID tests
// ---------------------------------------------------------------------------

func TestSteorraByID(t *testing.T) {
	hub := newTestHub(
		steorraConn{id: "steorra-a", addr: "a:1530", admin: &mockAdminClient{}},
		steorraConn{id: "steorra-b", addr: "b:1530", admin: &mockAdminClient{}},
	)

	t.Run("found", func(t *testing.T) {
		sc := hub.SteorraByID("steorra-a")
		if sc == nil {
			t.Fatal("expected steorra-a to be found")
		}
		if sc.id != "steorra-a" {
			t.Errorf("id = %q, want steorra-a", sc.id)
		}
		if sc.addr != "a:1530" {
			t.Errorf("addr = %q, want a:1530", sc.addr)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		sc := hub.SteorraByID("steorra-nonexistent")
		if sc != nil {
			t.Fatal("expected nil for nonexistent steorra")
		}
	})
}

// ---------------------------------------------------------------------------
// Single-steorra drill-down tests
// ---------------------------------------------------------------------------

func TestSteorraListEnvoys(t *testing.T) {
	mock := &mockAdminClient{
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
				{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
				{Id: "t2", Hostname: "web2.east.example.com", IpAddress: "10.0.1.2"},
			},
		},
	}

	hub := newTestHub(steorraConn{id: "steorra-east", addr: "east:1530", admin: mock})

	envoys, err := hub.SteorraListEnvoys(context.Background(), "steorra-east")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(envoys) != 2 {
		t.Fatalf("got %d envoys, want 2", len(envoys))
	}
	if envoys[0].Id != "t1" {
		t.Errorf("envoys[0].Id = %q, want t1", envoys[0].Id)
	}
	if envoys[1].Hostname != "web2.east.example.com" {
		t.Errorf("envoys[1].Hostname = %q, want web2.east.example.com", envoys[1].Hostname)
	}
}

func TestSteorraListRuns(t *testing.T) {
	mock := &mockAdminClient{
		listRunsResp: &pb.ListRunsResponse{
			Runs: []*pb.Run{
				{Id: "run-1", EnvoyId: "t1", Status: "success"},
				{Id: "run-2", EnvoyId: "t2", Status: "failure"},
			},
		},
	}

	hub := newTestHub(steorraConn{id: "steorra-east", addr: "east:1530", admin: mock})

	runs, err := hub.SteorraListRuns(context.Background(), "steorra-east", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Status != "success" {
		t.Errorf("runs[0].Status = %q, want success", runs[0].Status)
	}
}

func TestSteorraConvergenceSummary(t *testing.T) {
	mock := &mockAdminClient{
		convergenceResp: &pb.GetConvergenceSummaryResponse{
			Total: 10, Converged: 8, Relapsed: 1, Failed: 1, ConvergencePct: 80.0,
		},
	}

	hub := newTestHub(steorraConn{id: "steorra-east", addr: "east:1530", admin: mock})

	resp, err := hub.SteorraConvergenceSummary(context.Background(), "steorra-east")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Total != 10 {
		t.Errorf("Total = %d, want 10", resp.Total)
	}
	if resp.ConvergencePct != 80.0 {
		t.Errorf("ConvergencePct = %f, want 80.0", resp.ConvergencePct)
	}
}

func TestSteorraForcePush(t *testing.T) {
	mock := &mockAdminClient{
		forcePushResp: &pb.ForcePushResponse{EnvoysNotified: 5},
	}

	hub := newTestHub(steorraConn{id: "steorra-east", addr: "east:1530", admin: mock})

	count, err := hub.SteorraForcePush(context.Background(), "steorra-east", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 5 {
		t.Errorf("EnvoysNotified = %d, want 5", count)
	}
}

func TestSteorraNotFound(t *testing.T) {
	hub := newTestHub(steorraConn{
		id: "steorra-east", addr: "east:1530", admin: &mockAdminClient{},
	})
	ctx := context.Background()

	t.Run("SteorraListEnvoys", func(t *testing.T) {
		_, err := hub.SteorraListEnvoys(ctx, "steorra-missing")
		if err == nil {
			t.Fatal("expected error for missing steorra")
		}
	})

	t.Run("SteorraListRuns", func(t *testing.T) {
		_, err := hub.SteorraListRuns(ctx, "steorra-missing", 50)
		if err == nil {
			t.Fatal("expected error for missing steorra")
		}
	})

	t.Run("SteorraConvergenceSummary", func(t *testing.T) {
		_, err := hub.SteorraConvergenceSummary(ctx, "steorra-missing")
		if err == nil {
			t.Fatal("expected error for missing steorra")
		}
	})

	t.Run("SteorraForcePush", func(t *testing.T) {
		_, err := hub.SteorraForcePush(ctx, "steorra-missing", "")
		if err == nil {
			t.Fatal("expected error for missing steorra")
		}
	})
}

// ---------------------------------------------------------------------------
// ReassignEnvoy tests
// ---------------------------------------------------------------------------

func TestReassignEnvoy(t *testing.T) {
	srcMock := &mockAdminClient{
		getEnvoyResp: &pb.GetEnvoyResponse{
			Envoy: &pb.Envoy{
				Id:        "envoy-1",
				Hostname:  "web1.east.example.com",
				IpAddress: "10.0.1.1",
			},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollEnvoyResponse{EnvoyId: "envoy-1-new"},
	}

	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", admin: srcMock},
		steorraConn{id: "steorra-west", addr: "west:1530", admin: dstMock},
	)

	result, err := hub.ReassignEnvoy(context.Background(), "envoy-1", "steorra-east", "steorra-west")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewEnvoyID != "envoy-1-new" {
		t.Errorf("NewEnvoyID = %q, want envoy-1-new", result.NewEnvoyID)
	}
	if result.TargetAddr != "west:1530" {
		t.Errorf("TargetAddr = %q, want west:1530", result.TargetAddr)
	}

	// Verify EnrollEnvoy was called on the target steorra.
	if len(dstMock.enrollCalls) != 1 {
		t.Fatalf("expected 1 enroll call, got %d", len(dstMock.enrollCalls))
	}
	ec := dstMock.enrollCalls[0]
	if ec.Hostname != "web1.east.example.com" {
		t.Errorf("enroll Hostname = %q, want web1.east.example.com", ec.Hostname)
	}
	if ec.IpAddress != "10.0.1.1" {
		t.Errorf("enroll IpAddress = %q, want 10.0.1.1", ec.IpAddress)
	}

	// Verify SetReassignment was called on the source steorra.
	if len(srcMock.setReassignCalls) != 1 {
		t.Fatalf("expected 1 set-reassign call, got %d", len(srcMock.setReassignCalls))
	}
	rc := srcMock.setReassignCalls[0]
	if rc.EnvoyId != "envoy-1" {
		t.Errorf("reassign EnvoyId = %q, want envoy-1", rc.EnvoyId)
	}
	if rc.TargetAddr != "west:1530" {
		t.Errorf("reassign TargetAddr = %q, want west:1530", rc.TargetAddr)
	}
	if rc.NewEnvoyId != "envoy-1-new" {
		t.Errorf("reassign NewEnvoyId = %q, want envoy-1-new", rc.NewEnvoyId)
	}
}

func TestReassignEnvoySourceNotFound(t *testing.T) {
	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", admin: &mockAdminClient{}},
	)

	_, err := hub.ReassignEnvoy(context.Background(), "envoy-1", "steorra-missing", "steorra-east")
	if err == nil {
		t.Fatal("expected error for missing source steorra")
	}
}

func TestReassignEnvoyTargetNotFound(t *testing.T) {
	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", admin: &mockAdminClient{
			getEnvoyResp: &pb.GetEnvoyResponse{
				Envoy: &pb.Envoy{Id: "envoy-1", Hostname: "web1.east.example.com"},
			},
		}},
	)

	_, err := hub.ReassignEnvoy(context.Background(), "envoy-1", "steorra-east", "steorra-missing")
	if err == nil {
		t.Fatal("expected error for missing target steorra")
	}
}

// ---------------------------------------------------------------------------
// DrainSteorra tests
// ---------------------------------------------------------------------------

func TestDrainSteorra(t *testing.T) {
	srcMock := &mockAdminClient{
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
				{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
				{Id: "t2", Hostname: "web2.east.example.com", IpAddress: "10.0.1.2"},
			},
		},
		// GetEnvoy is called during ReassignEnvoy for each envoy.
		getEnvoyResp: &pb.GetEnvoyResponse{
			Envoy: &pb.Envoy{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollEnvoyResponse{EnvoyId: "new-id"},
	}

	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", admin: srcMock},
		steorraConn{id: "steorra-west", addr: "west:1530", patterns: []string{"*.east.example.com"}, admin: dstMock},
	)

	// Drain with an explicit target steorra.
	result, err := hub.DrainSteorra(context.Background(), "steorra-east", "steorra-west")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Moved != 2 {
		t.Errorf("Moved = %d, want 2", result.Moved)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}

	// Verify enroll was called twice on destination.
	if len(dstMock.enrollCalls) != 2 {
		t.Errorf("enroll calls = %d, want 2", len(dstMock.enrollCalls))
	}
}

func TestDrainSteorraAutoRoute(t *testing.T) {
	srcMock := &mockAdminClient{
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
				{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
			},
		},
		getEnvoyResp: &pb.GetEnvoyResponse{
			Envoy: &pb.Envoy{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollEnvoyResponse{EnvoyId: "new-t1"},
	}

	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", patterns: []string{"*.east.example.com"}, admin: srcMock},
		steorraConn{id: "steorra-west", addr: "west:1530", patterns: []string{"*.west.example.com"}, admin: dstMock},
	)

	// Drain without explicit target -- auto-route by hostname pattern.
	result, err := hub.DrainSteorra(context.Background(), "steorra-east", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Moved != 1 {
		t.Errorf("Moved = %d, want 1", result.Moved)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
}

func TestDrainSteorraPartialFailure(t *testing.T) {
	// Three envoys: two match a target pattern, one does not.
	srcMock := &mockAdminClient{
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
				{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
				{Id: "t2", Hostname: "orphan.nowhere.example.com", IpAddress: "10.0.3.1"},
				{Id: "t3", Hostname: "web2.west.example.com", IpAddress: "10.0.2.2"},
			},
		},
		getEnvoyResp: &pb.GetEnvoyResponse{
			Envoy: &pb.Envoy{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollEnvoyResponse{EnvoyId: "new-id"},
	}

	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", patterns: []string{"*.east.example.com"}, admin: srcMock},
		steorraConn{id: "steorra-west", addr: "west:1530", patterns: []string{"*.west.example.com"}, admin: dstMock},
	)

	// Drain without explicit target -- auto-route by hostname.
	// "orphan.nowhere.example.com" matches no pattern, so it should fail.
	result, err := hub.DrainSteorra(context.Background(), "steorra-east", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Moved != 2 {
		t.Errorf("Moved = %d, want 2", result.Moved)
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want 1", result.Failed)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(result.Errors))
	}
	if result.Errors[0] != "no target steorra for orphan.nowhere.example.com" {
		t.Errorf("Errors[0] = %q, want 'no target steorra for orphan.nowhere.example.com'", result.Errors[0])
	}
}

func TestDrainSteorraSkipsRevoked(t *testing.T) {
	srcMock := &mockAdminClient{
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
				{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1", Revoked: true},
			},
		},
	}

	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", admin: srcMock},
		steorraConn{id: "steorra-west", addr: "west:1530", patterns: []string{"*.west.example.com"}, admin: &mockAdminClient{}},
	)

	result, err := hub.DrainSteorra(context.Background(), "steorra-east", "steorra-west")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Moved != 0 {
		t.Errorf("Moved = %d, want 0 (revoked should be skipped)", result.Moved)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
}

func TestDrainSteorraNotFound(t *testing.T) {
	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", admin: &mockAdminClient{}},
	)

	_, err := hub.DrainSteorra(context.Background(), "steorra-missing", "")
	if err == nil {
		t.Fatal("expected error for missing steorra")
	}
}

func TestDrainSteorraReassignError(t *testing.T) {
	// Enrollment on target fails, so the envoy fails.
	srcMock := &mockAdminClient{
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
				{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
			},
		},
		getEnvoyResp: &pb.GetEnvoyResponse{
			Envoy: &pb.Envoy{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
		},
	}

	dstMock := &mockAdminClient{
		enrollErr: fmt.Errorf("enrollment rejected"),
	}

	hub := newTestHub(
		steorraConn{id: "steorra-east", addr: "east:1530", admin: srcMock},
		steorraConn{id: "steorra-west", addr: "west:1530", admin: dstMock},
	)

	result, err := hub.DrainSteorra(context.Background(), "steorra-east", "steorra-west")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Moved != 0 {
		t.Errorf("Moved = %d, want 0", result.Moved)
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want 1", result.Failed)
	}
}
