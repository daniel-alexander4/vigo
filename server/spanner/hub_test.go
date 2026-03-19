// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
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
	compliance *pb.GetComplianceSummaryResponse
	forcePush  int32
}

func (f *fakeAdminService) ListEnvoys(_ context.Context, _ *pb.ListEnvoysRequest) (*pb.ListEnvoysResponse, error) {
	return &pb.ListEnvoysResponse{Envoys: f.envoys}, nil
}

func (f *fakeAdminService) ListRuns(_ context.Context, _ *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	return &pb.ListRunsResponse{Runs: f.runs}, nil
}

func (f *fakeAdminService) GetComplianceSummary(_ context.Context, _ *pb.GetComplianceSummaryRequest) (*pb.GetComplianceSummaryResponse, error) {
	return f.compliance, nil
}

func (f *fakeAdminService) ForcePush(_ context.Context, _ *pb.ForcePushRequest) (*pb.ForcePushResponse, error) {
	return &pb.ForcePushResponse{EnvoysNotified: f.forcePush}, nil
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
	pb.RegisterVigoAdminServer(srv, svc)
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
	complianceResp  *pb.GetComplianceSummaryResponse
	complianceErr   error
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
	enrollCalls      []pb.EnrollEnvoyRequest
	setReassignCalls []pb.SetReassignmentRequest
}

func (m *mockAdminClient) GetComplianceSummary(_ context.Context, _ *pb.GetComplianceSummaryRequest, _ ...grpc.CallOption) (*pb.GetComplianceSummaryResponse, error) {
	return m.complianceResp, m.complianceErr
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
func (m *mockAdminClient) GetEnvoyCompliance(context.Context, *pb.GetEnvoyComplianceRequest, ...grpc.CallOption) (*pb.GetEnvoyComplianceResponse, error) {
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

func TestHub_AggregateListEnvoys(t *testing.T) {
	now := timestamppb.Now()

	spokeA := &fakeAdminService{
		envoys: []*pb.Envoy{
			{Id: "n1", Hostname: "web1.dc1", EnrolledAt: now},
			{Id: "n2", Hostname: "web2.dc1", EnrolledAt: now},
		},
	}
	spokeB := &fakeAdminService{
		envoys: []*pb.Envoy{
			{Id: "n3", Hostname: "web1.dc2", EnrolledAt: now},
		},
	}

	addrA := startFakeSpoke(t, spokeA)
	addrB := startFakeSpoke(t, spokeB)
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

func TestHub_AggregateComplianceSummary(t *testing.T) {
	spokeA := &fakeAdminService{
		compliance: &pb.GetComplianceSummaryResponse{
			Total: 10, Compliant: 8, RecurringDrift: 1, Errors: 1,
		},
	}
	spokeB := &fakeAdminService{
		compliance: &pb.GetComplianceSummaryResponse{
			Total: 5, Compliant: 4, LostContact: 1,
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
	if resp.RecurringDrift != 1 {
		t.Errorf("recurring_drift = %d, want 1", resp.RecurringDrift)
	}
	if resp.Errors != 1 {
		t.Errorf("errors = %d, want 1", resp.Errors)
	}
	if resp.LostContact != 1 {
		t.Errorf("lost_contact = %d, want 1", resp.LostContact)
	}
	// 12/(15-1 lost contact) = 85.7%
	if resp.CompliancePct < 85.0 || resp.CompliancePct > 86.5 {
		t.Errorf("compliance_pct = %f, want ~85.7 (lost contact excluded)", resp.CompliancePct)
	}
}

func TestHub_AggregateListRuns(t *testing.T) {
	now := timestamppb.Now()
	spokeA := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r1", EnvoyId: "n1", StartedAt: now, Status: "success"},
		},
	}
	spokeB := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r2", EnvoyId: "n3", StartedAt: now, Status: "failure"},
			{Id: "r3", EnvoyId: "n3", StartedAt: now, Status: "success"},
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
			Total:          20,
			Compliant:      15,
			RecurringDrift: 2,
			Errors:         1,
			LostContact:    1,
			NoData:         1,
			CompliancePct:  75.0,
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
	if s.EnvoyCount != 20 {
		t.Errorf("EnvoyCount = %d, want 20", s.EnvoyCount)
	}
	if s.Compliant != 15 {
		t.Errorf("Compliant = %d, want 15", s.Compliant)
	}
	if s.RecurringDrift != 2 {
		t.Errorf("RecurringDrift = %d, want 2", s.RecurringDrift)
	}
	if s.Errors != 1 {
		t.Errorf("Errors = %d, want 1", s.Errors)
	}
	if s.LostContact != 1 {
		t.Errorf("LostContact = %d, want 1", s.LostContact)
	}
	if s.NoData != 1 {
		t.Errorf("NoData = %d, want 1", s.NoData)
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
	if s.EnvoyCount != 0 {
		t.Errorf("EnvoyCount = %d, want 0 on error", s.EnvoyCount)
	}
}

func TestMultipleSpokeStatuses(t *testing.T) {
	hub := newTestHub(
		spokeConn{id: "spoke-a", addr: "a:1530", admin: &mockAdminClient{
			complianceResp: &pb.GetComplianceSummaryResponse{Total: 10, Compliant: 10, CompliancePct: 100},
		}},
		spokeConn{id: "spoke-b", addr: "b:1530", admin: &mockAdminClient{
			complianceResp: &pb.GetComplianceSummaryResponse{Total: 5, Compliant: 3, RecurringDrift: 2, CompliancePct: 60},
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
	if statuses[0].EnvoyCount != 10 {
		t.Errorf("spoke-a EnvoyCount = %d, want 10", statuses[0].EnvoyCount)
	}
	if statuses[1].RecurringDrift != 2 {
		t.Errorf("spoke-b RecurringDrift = %d, want 2", statuses[1].RecurringDrift)
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

func TestSpokeListEnvoys(t *testing.T) {
	mock := &mockAdminClient{
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
				{Id: "t1", Hostname: "web1.east.example.com", IpAddress: "10.0.1.1"},
				{Id: "t2", Hostname: "web2.east.example.com", IpAddress: "10.0.1.2"},
			},
		},
	}

	hub := newTestHub(spokeConn{id: "spoke-east", addr: "east:1530", admin: mock})

	envoys, err := hub.SpokeListEnvoys(context.Background(), "spoke-east")
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

func TestSpokeListRuns(t *testing.T) {
	mock := &mockAdminClient{
		listRunsResp: &pb.ListRunsResponse{
			Runs: []*pb.Run{
				{Id: "run-1", EnvoyId: "t1", Status: "success"},
				{Id: "run-2", EnvoyId: "t2", Status: "failure"},
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
			Total: 10, Compliant: 8, RecurringDrift: 1, Errors: 1, CompliancePct: 80.0,
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
		forcePushResp: &pb.ForcePushResponse{EnvoysNotified: 5},
	}

	hub := newTestHub(spokeConn{id: "spoke-east", addr: "east:1530", admin: mock})

	count, err := hub.SpokeForcePush(context.Background(), "spoke-east", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 5 {
		t.Errorf("EnvoysNotified = %d, want 5", count)
	}
}

func TestSpokeNotFound(t *testing.T) {
	hub := newTestHub(spokeConn{
		id: "spoke-east", addr: "east:1530", admin: &mockAdminClient{},
	})
	ctx := context.Background()

	t.Run("SpokeListEnvoys", func(t *testing.T) {
		_, err := hub.SpokeListEnvoys(ctx, "spoke-missing")
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
		spokeConn{id: "spoke-east", addr: "east:1530", admin: srcMock},
		spokeConn{id: "spoke-west", addr: "west:1530", admin: dstMock},
	)

	result, err := hub.ReassignEnvoy(context.Background(), "envoy-1", "spoke-east", "spoke-west")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewEnvoyID != "envoy-1-new" {
		t.Errorf("NewEnvoyID = %q, want envoy-1-new", result.NewEnvoyID)
	}
	if result.TargetAddr != "west:1530" {
		t.Errorf("TargetAddr = %q, want west:1530", result.TargetAddr)
	}

	// Verify EnrollEnvoy was called on the target spoke.
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
		spokeConn{id: "spoke-east", addr: "east:1530", admin: &mockAdminClient{}},
	)

	_, err := hub.ReassignEnvoy(context.Background(), "envoy-1", "spoke-missing", "spoke-east")
	if err == nil {
		t.Fatal("expected error for missing source spoke")
	}
}

func TestReassignEnvoyTargetNotFound(t *testing.T) {
	hub := newTestHub(
		spokeConn{id: "spoke-east", addr: "east:1530", admin: &mockAdminClient{
			getEnvoyResp: &pb.GetEnvoyResponse{
				Envoy: &pb.Envoy{Id: "envoy-1", Hostname: "web1.east.example.com"},
			},
		}},
	)

	_, err := hub.ReassignEnvoy(context.Background(), "envoy-1", "spoke-east", "spoke-missing")
	if err == nil {
		t.Fatal("expected error for missing target spoke")
	}
}

// ---------------------------------------------------------------------------
// DrainSpoke tests
// ---------------------------------------------------------------------------

func TestDrainSpoke(t *testing.T) {
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
		listEnvoysResp: &pb.ListEnvoysResponse{
			Envoys: []*pb.Envoy{
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
