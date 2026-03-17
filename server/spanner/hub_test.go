// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// fakeAdminService: real gRPC server for integration-style fan-out tests
// ---------------------------------------------------------------------------

// fakeAdminService implements HobnobAdminServer for testing hub fan-out.
type fakeAdminService struct {
	pb.UnimplementedHobnobAdminServer
	thralls    []*pb.Thrall
	runs       []*pb.Run
	compliance *pb.GetComplianceSummaryResponse
	forcePush  int32
}

func (f *fakeAdminService) ListThralls(_ context.Context, _ *pb.ListThrallsRequest) (*pb.ListThrallsResponse, error) {
	return &pb.ListThrallsResponse{Thralls: f.thralls}, nil
}

func (f *fakeAdminService) ListRuns(_ context.Context, _ *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	return &pb.ListRunsResponse{Runs: f.runs}, nil
}

func (f *fakeAdminService) GetComplianceSummary(_ context.Context, _ *pb.GetComplianceSummaryRequest) (*pb.GetComplianceSummaryResponse, error) {
	return f.compliance, nil
}

func (f *fakeAdminService) ForcePush(_ context.Context, _ *pb.ForcePushRequest) (*pb.ForcePushResponse, error) {
	return &pb.ForcePushResponse{ThrallsNotified: f.forcePush}, nil
}

// startFakeSpoke starts a gRPC server with a fake admin service and returns
// its address and a cleanup function.
func startFakeSpoke(t *testing.T, svc *fakeAdminService) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterHobnobAdminServer(srv, svc)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String()
}

// newGRPCTestHub creates a Hub connected to test spokes via insecure transport.
func newGRPCTestHub(t *testing.T, addrs []string) *Hub {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h := &Hub{spokes: make([]spokeConn, 0, len(addrs))}
	for i, addr := range addrs {
		conn, err := grpc.DialContext(ctx, addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		if err != nil {
			t.Fatalf("connecting to fake spoke %d: %v", i, err)
		}
		t.Cleanup(func() { conn.Close() })
		h.spokes = append(h.spokes, spokeConn{
			id:    fmt.Sprintf("spoke%c", 'A'+i),
			addr:  addr,
			conn:  conn,
			admin: pb.NewHobnobAdminClient(conn),
		})
	}
	return h
}

// ---------------------------------------------------------------------------
// mockAdminClient: in-process mock for unit tests (no real gRPC server)
// ---------------------------------------------------------------------------

// mockAdminClient implements pb.HobnobAdminClient with canned responses.
type mockAdminClient struct {
	complianceResp  *pb.GetComplianceSummaryResponse
	complianceErr   error
	listThrallsResp *pb.ListThrallsResponse
	listThrallsErr  error
	listRunsResp    *pb.ListRunsResponse
	listRunsErr     error
	forcePushResp   *pb.ForcePushResponse
	forcePushErr    error
	getThrallResp   *pb.GetThrallResponse
	getThrallErr    error
	enrollResp      *pb.EnrollThrallResponse
	enrollErr       error
	setReassignResp *pb.SetReassignmentResponse
	setReassignErr  error

	// Track calls for assertions.
	enrollCalls      []pb.EnrollThrallRequest
	setReassignCalls []pb.SetReassignmentRequest
}

func (m *mockAdminClient) GetComplianceSummary(_ context.Context, _ *pb.GetComplianceSummaryRequest, _ ...grpc.CallOption) (*pb.GetComplianceSummaryResponse, error) {
	return m.complianceResp, m.complianceErr
}

func (m *mockAdminClient) ListThralls(_ context.Context, _ *pb.ListThrallsRequest, _ ...grpc.CallOption) (*pb.ListThrallsResponse, error) {
	return m.listThrallsResp, m.listThrallsErr
}

func (m *mockAdminClient) ListRuns(_ context.Context, _ *pb.ListRunsRequest, _ ...grpc.CallOption) (*pb.ListRunsResponse, error) {
	return m.listRunsResp, m.listRunsErr
}

func (m *mockAdminClient) ForcePush(_ context.Context, _ *pb.ForcePushRequest, _ ...grpc.CallOption) (*pb.ForcePushResponse, error) {
	return m.forcePushResp, m.forcePushErr
}

func (m *mockAdminClient) GetThrall(_ context.Context, _ *pb.GetThrallRequest, _ ...grpc.CallOption) (*pb.GetThrallResponse, error) {
	return m.getThrallResp, m.getThrallErr
}

func (m *mockAdminClient) EnrollThrall(_ context.Context, req *pb.EnrollThrallRequest, _ ...grpc.CallOption) (*pb.EnrollThrallResponse, error) {
	m.enrollCalls = append(m.enrollCalls, *req)
	return m.enrollResp, m.enrollErr
}

func (m *mockAdminClient) SetReassignment(_ context.Context, req *pb.SetReassignmentRequest, _ ...grpc.CallOption) (*pb.SetReassignmentResponse, error) {
	m.setReassignCalls = append(m.setReassignCalls, *req)
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
func (m *mockAdminClient) RevokeThrall(context.Context, *pb.RevokeThrallRequest, ...grpc.CallOption) (*pb.RevokeThrallResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) DeleteThrall(context.Context, *pb.DeleteThrallRequest, ...grpc.CallOption) (*pb.DeleteThrallResponse, error) {
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
func (m *mockAdminClient) GetThrallCompliance(context.Context, *pb.GetThrallComplianceRequest, ...grpc.CallOption) (*pb.GetThrallComplianceResponse, error) {
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
func (m *mockAdminClient) PromoteEnv(context.Context, *pb.PromoteEnvRequest, ...grpc.CallOption) (*pb.PromoteEnvResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) ReportSpokeStatus(context.Context, *pb.SpokeStatusReport, ...grpc.CallOption) (*pb.SpokeStatusAck, error) {
	return nil, status.Error(codes.Unimplemented, "not mocked")
}
func (m *mockAdminClient) SyncConfig(context.Context, *pb.SyncConfigRequest, ...grpc.CallOption) (*pb.SyncConfigResponse, error) {
	return &pb.SyncConfigResponse{}, nil
}

// newTestHub builds a Hub with mock spoke connections (no real gRPC).
func newTestHub(spokes ...spokeConn) *Hub {
	return &Hub{spokes: spokes}
}

// ---------------------------------------------------------------------------
// Fan-out integration tests (existing, using real gRPC)
// ---------------------------------------------------------------------------

func TestHub_AggregateListThralls(t *testing.T) {
	now := timestamppb.Now()

	spokeA := &fakeAdminService{
		thralls: []*pb.Thrall{
			{Id: "n1", Hostname: "web1.dc1", EnrolledAt: now},
			{Id: "n2", Hostname: "web2.dc1", EnrolledAt: now},
		},
	}
	spokeB := &fakeAdminService{
		thralls: []*pb.Thrall{
			{Id: "n3", Hostname: "web1.dc2", EnrolledAt: now},
		},
	}

	addrA := startFakeSpoke(t, spokeA)
	addrB := startFakeSpoke(t, spokeB)
	hub := newGRPCTestHub(t, []string{addrA, addrB})

	thralls, err := hub.AggregateListThralls(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(thralls) != 3 {
		t.Fatalf("got %d thralls, want 3", len(thralls))
	}

	ids := map[string]bool{}
	for _, n := range thralls {
		ids[n.Id] = true
	}
	for _, want := range []string{"n1", "n2", "n3"} {
		if !ids[want] {
			t.Errorf("missing thrall %s", want)
		}
	}
}

func TestHub_AggregateComplianceSummary(t *testing.T) {
	spokeA := &fakeAdminService{
		compliance: &pb.GetComplianceSummaryResponse{
			Total: 10, Compliant: 8, Drifted: 1, Failing: 1,
		},
	}
	spokeB := &fakeAdminService{
		compliance: &pb.GetComplianceSummaryResponse{
			Total: 5, Compliant: 4, Stale: 1,
		},
	}

	addrA := startFakeSpoke(t, spokeA)
	addrB := startFakeSpoke(t, spokeB)
	hub := newGRPCTestHub(t, []string{addrA, addrB})

	resp, err := hub.AggregateComplianceSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if resp.Total != 15 {
		t.Errorf("total = %d, want 15", resp.Total)
	}
	if resp.Compliant != 12 {
		t.Errorf("compliant = %d, want 12", resp.Compliant)
	}
	if resp.Drifted != 1 {
		t.Errorf("drifted = %d, want 1", resp.Drifted)
	}
	if resp.Failing != 1 {
		t.Errorf("failing = %d, want 1", resp.Failing)
	}
	if resp.Stale != 1 {
		t.Errorf("stale = %d, want 1", resp.Stale)
	}
	// 12/(15-1 stale) = 85.7%
	if resp.CompliancePct < 85.0 || resp.CompliancePct > 86.5 {
		t.Errorf("compliance_pct = %f, want ~85.7 (stale excluded)", resp.CompliancePct)
	}
}

func TestHub_AggregateListRuns(t *testing.T) {
	now := timestamppb.Now()
	spokeA := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r1", ThrallId: "n1", StartedAt: now, Status: "success"},
		},
	}
	spokeB := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r2", ThrallId: "n3", StartedAt: now, Status: "failure"},
			{Id: "r3", ThrallId: "n3", StartedAt: now, Status: "success"},
		},
	}

	addrA := startFakeSpoke(t, spokeA)
	addrB := startFakeSpoke(t, spokeB)
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
	spokeA := &fakeAdminService{forcePush: 5}
	spokeB := &fakeAdminService{forcePush: 3}

	addrA := startFakeSpoke(t, spokeA)
	addrB := startFakeSpoke(t, spokeB)
	hub := newGRPCTestHub(t, []string{addrA, addrB})

	total, err := hub.ForwardForcePush(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 8 {
		t.Errorf("total = %d, want 8", total)
	}
}

func TestHub_SpokeCount(t *testing.T) {
	hub := &Hub{spokes: make([]spokeConn, 3)}
	if hub.SpokeCount() != 3 {
		t.Errorf("SpokeCount() = %d, want 3", hub.SpokeCount())
	}
}

// ---------------------------------------------------------------------------
// Enriched spoke status tests (mock client, no real gRPC)
// ---------------------------------------------------------------------------

func TestSpokeStatusCompliance(t *testing.T) {
	mock := &mockAdminClient{
		complianceResp: &pb.GetComplianceSummaryResponse{
			Total:         20,
			Compliant:     15,
			Drifted:       2,
			Failing:       1,
			Stale:         1,
			Unknown:       1,
			CompliancePct: 75.0,
		},
	}

	hub := newTestHub(spokeConn{
		id:       "spoke-east",
		addr:     "east.example.com:1530",
		patterns: []string{"*.east.example.com"},
		admin:    mock,
	})

	statuses := hub.SpokeStatuses(context.Background())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	s := statuses[0]
	if s.ID != "spoke-east" {
		t.Errorf("ID = %q, want spoke-east", s.ID)
	}
	if s.Addr != "east.example.com:1530" {
		t.Errorf("Addr = %q, want east.example.com:1530", s.Addr)
	}
	if !s.Healthy {
		t.Error("expected spoke to be healthy")
	}
	if s.ThrallCount != 20 {
		t.Errorf("ThrallCount = %d, want 20", s.ThrallCount)
	}
	if s.Compliant != 15 {
		t.Errorf("Compliant = %d, want 15", s.Compliant)
	}
	if s.Drifted != 2 {
		t.Errorf("Drifted = %d, want 2", s.Drifted)
	}
	if s.Failing != 1 {
		t.Errorf("Failing = %d, want 1", s.Failing)
	}
	if s.Stale != 1 {
		t.Errorf("Stale = %d, want 1", s.Stale)
	}
	if s.Unknown != 1 {
		t.Errorf("Unknown = %d, want 1", s.Unknown)
	}
	if s.CompliancePct != 75.0 {
		t.Errorf("CompliancePct = %f, want 75.0", s.CompliancePct)
	}
	if s.Error != "" {
		t.Errorf("Error = %q, want empty", s.Error)
	}
	if len(s.Patterns) != 1 || s.Patterns[0] != "*.east.example.com" {
		t.Errorf("Patterns = %v, want [*.east.example.com]", s.Patterns)
	}
}

func TestSpokeStatusLatency(t *testing.T) {
	mock := &mockAdminClient{
		complianceResp: &pb.GetComplianceSummaryResponse{Total: 5, Compliant: 5},
	}

	hub := newTestHub(spokeConn{
		id:    "spoke-west",
		addr:  "west.example.com:1530",
		admin: mock,
	})

	statuses := hub.SpokeStatuses(context.Background())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	// Latency is non-negative (the mock returns instantly, so >= 0).
	if statuses[0].LatencyMs < 0 {
		t.Errorf("LatencyMs = %d, want >= 0", statuses[0].LatencyMs)
	}
	if !statuses[0].Healthy {
		t.Error("expected spoke to be healthy")
	}
}

func TestSpokeStatusError(t *testing.T) {
	mock := &mockAdminClient{
		complianceErr: fmt.Errorf("connection refused"),
	}

	hub := newTestHub(spokeConn{
		id:    "spoke-down",
		addr:  "down:1530",
		admin: mock,
	})

	statuses := hub.SpokeStatuses(context.Background())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	s := statuses[0]
	if s.Healthy {
		t.Error("expected spoke to be unhealthy")
	}
	if s.Error == "" {
		t.Error("expected Error to be populated")
	}
	if s.ThrallCount != 0 {
		t.Errorf("ThrallCount = %d, want 0 on error", s.ThrallCount)
	}
}

func TestMultipleSpokeStatuses(t *testing.T) {
	hub := newTestHub(
		spokeConn{id: "spoke-a", addr: "a:1530", admin: &mockAdminClient{
			complianceResp: &pb.GetComplianceSummaryResponse{Total: 10, Compliant: 10, CompliancePct: 100},
		}},
		spokeConn{id: "spoke-b", addr: "b:1530", admin: &mockAdminClient{
			complianceResp: &pb.GetComplianceSummaryResponse{Total: 5, Compliant: 3, Drifted: 2, CompliancePct: 60},
		}},
	)

	statuses := hub.SpokeStatuses(context.Background())
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// Order matches spoke order.
	if statuses[0].ID != "spoke-a" || statuses[1].ID != "spoke-b" {
		t.Errorf("status IDs = [%s, %s], want [spoke-a, spoke-b]", statuses[0].ID, statuses[1].ID)
	}
	if statuses[0].ThrallCount != 10 {
		t.Errorf("spoke-a ThrallCount = %d, want 10", statuses[0].ThrallCount)
	}
	if statuses[1].Drifted != 2 {
		t.Errorf("spoke-b Drifted = %d, want 2", statuses[1].Drifted)
	}
}

// ---------------------------------------------------------------------------
// SpokeByID tests
// ---------------------------------------------------------------------------

func TestSpokeByID(t *testing.T) {
	hub := newTestHub(
		spokeConn{id: "spoke-a", addr: "a:1530", admin: &mockAdminClient{}},
		spokeConn{id: "spoke-b", addr: "b:1530", admin: &mockAdminClient{}},
	)

	t.Run("found", func(t *testing.T) {
		sc := hub.SpokeByID("spoke-a")
		if sc == nil {
			t.Fatal("expected spoke-a to be found")
		}
		if sc.id != "spoke-a" {
			t.Errorf("id = %q, want spoke-a", sc.id)
		}
		if sc.addr != "a:1530" {
			t.Errorf("addr = %q, want a:1530", sc.addr)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		sc := hub.SpokeByID("spoke-nonexistent")
		if sc != nil {
			t.Fatal("expected nil for nonexistent spoke")
		}
	})
}

// ---------------------------------------------------------------------------
// Single-spoke drill-down tests
// ---------------------------------------------------------------------------

func TestSpokeListThralls(t *testing.T) {
	mock := &mockAdminClient{
		listThrallsResp: &pb.ListThrallsResponse{
			Thralls: []*pb.Thrall{
				{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
				{Id: "t2", Hostname: "web2.east.example.com", IpAddress: "10.0.1.2"},
			},
		},
	}

	hub := newTestHub(spokeConn{id: "spoke-east", addr: "east:1530", admin: mock})

	thralls, err := hub.SpokeListThralls(context.Background(), "spoke-east")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(thralls) != 2 {
		t.Fatalf("got %d thralls, want 2", len(thralls))
	}
	if thralls[0].Id != "t1" {
		t.Errorf("thralls[0].Id = %q, want t1", thralls[0].Id)
	}
	if thralls[1].Hostname != "web2.east.example.com" {
		t.Errorf("thralls[1].Hostname = %q, want web2.east.example.com", thralls[1].Hostname)
	}
}

func TestSpokeListRuns(t *testing.T) {
	mock := &mockAdminClient{
		listRunsResp: &pb.ListRunsResponse{
			Runs: []*pb.Run{
				{Id: "run-1", ThrallId: "t1", Status: "success"},
				{Id: "run-2", ThrallId: "t2", Status: "failure"},
			},
		},
	}

	hub := newTestHub(spokeConn{id: "spoke-east", addr: "east:1530", admin: mock})

	runs, err := hub.SpokeListRuns(context.Background(), "spoke-east", 10)
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

func TestSpokeComplianceSummary(t *testing.T) {
	mock := &mockAdminClient{
		complianceResp: &pb.GetComplianceSummaryResponse{
			Total: 10, Compliant: 8, Drifted: 1, Failing: 1, CompliancePct: 80.0,
		},
	}

	hub := newTestHub(spokeConn{id: "spoke-east", addr: "east:1530", admin: mock})

	resp, err := hub.SpokeComplianceSummary(context.Background(), "spoke-east")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Total != 10 {
		t.Errorf("Total = %d, want 10", resp.Total)
	}
	if resp.CompliancePct != 80.0 {
		t.Errorf("CompliancePct = %f, want 80.0", resp.CompliancePct)
	}
}

func TestSpokeForcePush(t *testing.T) {
	mock := &mockAdminClient{
		forcePushResp: &pb.ForcePushResponse{ThrallsNotified: 5},
	}

	hub := newTestHub(spokeConn{id: "spoke-east", addr: "east:1530", admin: mock})

	count, err := hub.SpokeForcePush(context.Background(), "spoke-east", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 5 {
		t.Errorf("ThrallsNotified = %d, want 5", count)
	}
}

func TestSpokeNotFound(t *testing.T) {
	hub := newTestHub(spokeConn{
		id: "spoke-east", addr: "east:1530", admin: &mockAdminClient{},
	})
	ctx := context.Background()

	t.Run("SpokeListThralls", func(t *testing.T) {
		_, err := hub.SpokeListThralls(ctx, "spoke-missing")
		if err == nil {
			t.Fatal("expected error for missing spoke")
		}
	})

	t.Run("SpokeListRuns", func(t *testing.T) {
		_, err := hub.SpokeListRuns(ctx, "spoke-missing", 50)
		if err == nil {
			t.Fatal("expected error for missing spoke")
		}
	})

	t.Run("SpokeComplianceSummary", func(t *testing.T) {
		_, err := hub.SpokeComplianceSummary(ctx, "spoke-missing")
		if err == nil {
			t.Fatal("expected error for missing spoke")
		}
	})

	t.Run("SpokeForcePush", func(t *testing.T) {
		_, err := hub.SpokeForcePush(ctx, "spoke-missing", "")
		if err == nil {
			t.Fatal("expected error for missing spoke")
		}
	})
}

// ---------------------------------------------------------------------------
// ReassignThrall tests
// ---------------------------------------------------------------------------

func TestReassignThrall(t *testing.T) {
	srcMock := &mockAdminClient{
		getThrallResp: &pb.GetThrallResponse{
			Thrall: &pb.Thrall{
				Id:        "thrall-1",
				Hostname:  "web1.east.example.com",
				IpAddress: "10.0.1.1",
			},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollThrallResponse{ThrallId: "thrall-1-new"},
	}

	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: srcMock},
		spokeConn{id: "spoke-west", addr: "west:1530", admin: dstMock},
	)

	result, err := hub.ReassignThrall(context.Background(), "thrall-1", "spoke-east", "spoke-west")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewThrallID != "thrall-1-new" {
		t.Errorf("NewThrallID = %q, want thrall-1-new", result.NewThrallID)
	}
	if result.TargetAddr != "west:1530" {
		t.Errorf("TargetAddr = %q, want west:1530", result.TargetAddr)
	}

	// Verify EnrollThrall was called on the target spoke.
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

	// Verify SetReassignment was called on the source spoke.
	if len(srcMock.setReassignCalls) != 1 {
		t.Fatalf("expected 1 set-reassign call, got %d", len(srcMock.setReassignCalls))
	}
	rc := srcMock.setReassignCalls[0]
	if rc.ThrallId != "thrall-1" {
		t.Errorf("reassign ThrallId = %q, want thrall-1", rc.ThrallId)
	}
	if rc.TargetAddr != "west:1530" {
		t.Errorf("reassign TargetAddr = %q, want west:1530", rc.TargetAddr)
	}
	if rc.NewThrallId != "thrall-1-new" {
		t.Errorf("reassign NewThrallId = %q, want thrall-1-new", rc.NewThrallId)
	}
}

func TestReassignThrallSourceNotFound(t *testing.T) {
	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: &mockAdminClient{}},
	)

	_, err := hub.ReassignThrall(context.Background(), "thrall-1", "spoke-missing", "spoke-east")
	if err == nil {
		t.Fatal("expected error for missing source spoke")
	}
}

func TestReassignThrallTargetNotFound(t *testing.T) {
	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: &mockAdminClient{
			getThrallResp: &pb.GetThrallResponse{
				Thrall: &pb.Thrall{Id: "thrall-1", Hostname: "web1.east.example.com"},
			},
		}},
	)

	_, err := hub.ReassignThrall(context.Background(), "thrall-1", "spoke-east", "spoke-missing")
	if err == nil {
		t.Fatal("expected error for missing target spoke")
	}
}

// ---------------------------------------------------------------------------
// DrainSpoke tests
// ---------------------------------------------------------------------------

func TestDrainSpoke(t *testing.T) {
	srcMock := &mockAdminClient{
		listThrallsResp: &pb.ListThrallsResponse{
			Thralls: []*pb.Thrall{
				{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
				{Id: "t2", Hostname: "web2.east.example.com", IpAddress: "10.0.1.2"},
			},
		},
		// GetThrall is called during ReassignThrall for each thrall.
		getThrallResp: &pb.GetThrallResponse{
			Thrall: &pb.Thrall{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollThrallResponse{ThrallId: "new-id"},
	}

	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: srcMock},
		spokeConn{id: "spoke-west", addr: "west:1530", patterns: []string{"*.east.example.com"}, admin: dstMock},
	)

	// Drain with an explicit target spoke.
	result, err := hub.DrainSpoke(context.Background(), "spoke-east", "spoke-west")
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

func TestDrainSpokeAutoRoute(t *testing.T) {
	srcMock := &mockAdminClient{
		listThrallsResp: &pb.ListThrallsResponse{
			Thralls: []*pb.Thrall{
				{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
			},
		},
		getThrallResp: &pb.GetThrallResponse{
			Thrall: &pb.Thrall{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollThrallResponse{ThrallId: "new-t1"},
	}

	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", patterns: []string{"*.east.example.com"}, admin: srcMock},
		spokeConn{id: "spoke-west", addr: "west:1530", patterns: []string{"*.west.example.com"}, admin: dstMock},
	)

	// Drain without explicit target -- auto-route by hostname pattern.
	result, err := hub.DrainSpoke(context.Background(), "spoke-east", "")
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

func TestDrainSpokePartialFailure(t *testing.T) {
	// Three thralls: two match a target pattern, one does not.
	srcMock := &mockAdminClient{
		listThrallsResp: &pb.ListThrallsResponse{
			Thralls: []*pb.Thrall{
				{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
				{Id: "t2", Hostname: "orphan.nowhere.example.com", IpAddress: "10.0.3.1"},
				{Id: "t3", Hostname: "web2.west.example.com", IpAddress: "10.0.2.2"},
			},
		},
		getThrallResp: &pb.GetThrallResponse{
			Thrall: &pb.Thrall{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1"},
		},
		setReassignResp: &pb.SetReassignmentResponse{},
	}

	dstMock := &mockAdminClient{
		enrollResp: &pb.EnrollThrallResponse{ThrallId: "new-id"},
	}

	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", patterns: []string{"*.east.example.com"}, admin: srcMock},
		spokeConn{id: "spoke-west", addr: "west:1530", patterns: []string{"*.west.example.com"}, admin: dstMock},
	)

	// Drain without explicit target -- auto-route by hostname.
	// "orphan.nowhere.example.com" matches no pattern, so it should fail.
	result, err := hub.DrainSpoke(context.Background(), "spoke-east", "")
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
	if result.Errors[0] != "no target spoke for orphan.nowhere.example.com" {
		t.Errorf("Errors[0] = %q, want 'no target spoke for orphan.nowhere.example.com'", result.Errors[0])
	}
}

func TestDrainSpokeSkipsRevoked(t *testing.T) {
	srcMock := &mockAdminClient{
		listThrallsResp: &pb.ListThrallsResponse{
			Thralls: []*pb.Thrall{
				{Id: "t1", Hostname: "web1.west.example.com", IpAddress: "10.0.2.1", Revoked: true},
			},
		},
	}

	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: srcMock},
		spokeConn{id: "spoke-west", addr: "west:1530", patterns: []string{"*.west.example.com"}, admin: &mockAdminClient{}},
	)

	result, err := hub.DrainSpoke(context.Background(), "spoke-east", "spoke-west")
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

func TestDrainSpokeNotFound(t *testing.T) {
	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: &mockAdminClient{}},
	)

	_, err := hub.DrainSpoke(context.Background(), "spoke-missing", "")
	if err == nil {
		t.Fatal("expected error for missing spoke")
	}
}

func TestDrainSpokeReassignError(t *testing.T) {
	// Enrollment on target fails, so the thrall fails.
	srcMock := &mockAdminClient{
		listThrallsResp: &pb.ListThrallsResponse{
			Thralls: []*pb.Thrall{
				{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
			},
		},
		getThrallResp: &pb.GetThrallResponse{
			Thrall: &pb.Thrall{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
		},
	}

	dstMock := &mockAdminClient{
		enrollErr: fmt.Errorf("enrollment rejected"),
	}

	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: srcMock},
		spokeConn{id: "spoke-west", addr: "west:1530", admin: dstMock},
	)

	result, err := hub.DrainSpoke(context.Background(), "spoke-east", "spoke-west")
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
