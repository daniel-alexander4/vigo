package federation

import (
	"context"
	"net"
	"testing"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeAdminService implements HobnobAdminServer for testing hub fan-out.
type fakeAdminService struct {
	pb.UnimplementedHobnobAdminServer
	nodes      []*pb.Node
	runs       []*pb.Run
	compliance *pb.GetComplianceSummaryResponse
	forcePush  int32
}

func (f *fakeAdminService) ListNodes(_ context.Context, _ *pb.ListNodesRequest) (*pb.ListNodesResponse, error) {
	return &pb.ListNodesResponse{Nodes: f.nodes}, nil
}

func (f *fakeAdminService) ListRuns(_ context.Context, _ *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	return &pb.ListRunsResponse{Runs: f.runs}, nil
}

func (f *fakeAdminService) GetComplianceSummary(_ context.Context, _ *pb.GetComplianceSummaryRequest) (*pb.GetComplianceSummaryResponse, error) {
	return f.compliance, nil
}

func (f *fakeAdminService) ForcePush(_ context.Context, _ *pb.ForcePushRequest) (*pb.ForcePushResponse, error) {
	return &pb.ForcePushResponse{NodesNotified: f.forcePush}, nil
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

// newTestHub creates a Hub connected to test spokes via insecure transport.
func newTestHub(t *testing.T, addrs []string) *Hub {
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
			id:    "spoke" + string(rune('A'+i)),
			addr:  addr,
			conn:  conn,
			admin: pb.NewHobnobAdminClient(conn),
		})
	}
	return h
}

func TestHub_AggregateListNodes(t *testing.T) {
	now := timestamppb.Now()

	spokeA := &fakeAdminService{
		nodes: []*pb.Node{
			{Id: "n1", Hostname: "web1.dc1", EnrolledAt: now},
			{Id: "n2", Hostname: "web2.dc1", EnrolledAt: now},
		},
	}
	spokeB := &fakeAdminService{
		nodes: []*pb.Node{
			{Id: "n3", Hostname: "web1.dc2", EnrolledAt: now},
		},
	}

	addrA := startFakeSpoke(t, spokeA)
	addrB := startFakeSpoke(t, spokeB)
	hub := newTestHub(t, []string{addrA, addrB})

	nodes, err := hub.AggregateListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(nodes))
	}

	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.Id] = true
	}
	for _, want := range []string{"n1", "n2", "n3"} {
		if !ids[want] {
			t.Errorf("missing node %s", want)
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
	hub := newTestHub(t, []string{addrA, addrB})

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
	// 12/15 = 80%
	if resp.CompliancePct < 79.9 || resp.CompliancePct > 80.1 {
		t.Errorf("compliance_pct = %f, want ~80", resp.CompliancePct)
	}
}

func TestHub_AggregateListRuns(t *testing.T) {
	now := timestamppb.Now()
	spokeA := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r1", NodeId: "n1", StartedAt: now, Status: "success"},
		},
	}
	spokeB := &fakeAdminService{
		runs: []*pb.Run{
			{Id: "r2", NodeId: "n3", StartedAt: now, Status: "failure"},
			{Id: "r3", NodeId: "n3", StartedAt: now, Status: "success"},
		},
	}

	addrA := startFakeSpoke(t, spokeA)
	addrB := startFakeSpoke(t, spokeB)
	hub := newTestHub(t, []string{addrA, addrB})

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
	hub := newTestHub(t, []string{addrA, addrB})

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
