package checkin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isogov/isogov/agent/modules"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"google.golang.org/grpc"
)

func TestJitteredDuration_WithinRange(t *testing.T) {
	base := 30 * time.Second
	jitterPct := 20

	minExpected := base - time.Duration(float64(base)*float64(jitterPct)/100.0)
	maxExpected := base + time.Duration(float64(base)*float64(jitterPct)/100.0)

	for i := 0; i < 100; i++ {
		result := jitteredDuration(base, jitterPct)
		if result < minExpected || result > maxExpected {
			t.Errorf("jitteredDuration(%v, %d) = %v, want [%v, %v]",
				base, jitterPct, result, minExpected, maxExpected)
		}
	}
}

func TestJitteredDuration_ZeroJitter(t *testing.T) {
	base := 30 * time.Second
	result := jitteredDuration(base, 0)
	if result != base {
		t.Errorf("jitteredDuration(%v, 0) = %v, want %v", base, result, base)
	}
}

func TestJitteredDuration_NegativeJitter(t *testing.T) {
	base := 30 * time.Second
	result := jitteredDuration(base, -5)
	if result != base {
		t.Errorf("jitteredDuration(%v, -5) = %v, want %v", base, result, base)
	}
}

func TestStateFingerprint_Deterministic(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{
			"family": "linux",
			"distro": "ubuntu",
		},
	}

	fp1 := stateFingerprint(facts)
	fp2 := stateFingerprint(facts)

	if fp1 != fp2 {
		t.Errorf("stateFingerprint not deterministic: %q != %q", fp1, fp2)
	}

	if len(fp1) != 64 { // SHA256 hex = 64 chars
		t.Errorf("stateFingerprint length = %d, want 64", len(fp1))
	}
}

func TestResultsToProto(t *testing.T) {
	results := []modules.ResourceResult{
		{
			ModuleName:   "nginx",
			ResourceType: "file",
			ResourceName: "/etc/nginx/nginx.conf",
			Action:       modules.ActionUpdated,
			Changed:      true,
			DurationMs:   42,
		},
		{
			ModuleName:   "nginx",
			ResourceType: "service",
			ResourceName: "nginx",
			Action:       modules.ActionNone,
			Changed:      false,
			Error:        errors.New("service not found"),
			DurationMs:   5,
		},
	}

	proto := resultsToProto(results)
	if len(proto) != 2 {
		t.Fatalf("len(proto) = %d, want 2", len(proto))
	}

	if proto[0].ModuleName != "nginx" {
		t.Errorf("proto[0].ModuleName = %q, want %q", proto[0].ModuleName, "nginx")
	}
	if proto[0].ActionTaken != "updated" {
		t.Errorf("proto[0].ActionTaken = %q, want %q", proto[0].ActionTaken, "updated")
	}
	if !proto[0].Changed {
		t.Error("proto[0].Changed = false, want true")
	}
	if proto[0].ErrorMessage != "" {
		t.Errorf("proto[0].ErrorMessage = %q, want empty", proto[0].ErrorMessage)
	}

	if proto[1].ErrorMessage != "service not found" {
		t.Errorf("proto[1].ErrorMessage = %q, want %q", proto[1].ErrorMessage, "service not found")
	}
}

// mockAgentClient implements pb.IsgovAgentClient for testing.
type mockAgentClient struct {
	checkInResp  *pb.CheckInResponse
	checkInErr   error
	reportCalled bool
	factsCalled  bool
	factsErr     error
}

func (m *mockAgentClient) Register(_ context.Context, _ *pb.RegisterRequest, _ ...grpc.CallOption) (*pb.RegisterResponse, error) {
	return nil, nil
}

func (m *mockAgentClient) CheckIn(_ context.Context, _ *pb.CheckInRequest, _ ...grpc.CallOption) (*pb.CheckInResponse, error) {
	return m.checkInResp, m.checkInErr
}

func (m *mockAgentClient) ReportResult(_ context.Context, _ *pb.ReportResultRequest, _ ...grpc.CallOption) (*pb.ReportResultResponse, error) {
	m.reportCalled = true
	return &pb.ReportResultResponse{Acknowledged: true}, nil
}

func (m *mockAgentClient) ReportFacts(_ context.Context, _ *pb.ReportFactsRequest, _ ...grpc.CallOption) (*pb.ReportFactsResponse, error) {
	m.factsCalled = true
	return &pb.ReportFactsResponse{Acknowledged: true}, m.factsErr
}

// newMockClient creates a Client with a mock gRPC agent for testing.
func newMockClient(mock *mockAgentClient) *Client {
	return &Client{
		agent:      mock,
		clientID:   "test-client-id",
		privateKey: make([]byte, 64), // fake key, signing will produce deterministic output
	}
}

func TestRunOnce_DryRun_DoesNotCallReportResult(t *testing.T) {
	mock := &mockAgentClient{
		checkInResp: &pb.CheckInResponse{
			Modules:       nil,
			BaseInterval:  "30m",
			JitterPercent: 10,
		},
	}

	client := newMockClient(mock)
	executors := map[string]modules.Executor{}
	factsMap := map[string]any{}

	result, err := RunOnce(context.Background(), client, executors, factsMap, true, "")
	if err != nil {
		t.Fatalf("RunOnce dry-run: %v", err)
	}

	if mock.reportCalled {
		t.Error("RunOnce dry-run called ReportResult, expected skip")
	}

	if result == nil {
		t.Fatal("RunOnce returned nil result")
	}
}

func TestRunOnce_ReportsResults(t *testing.T) {
	mock := &mockAgentClient{
		checkInResp: &pb.CheckInResponse{
			Modules:       nil,
			BaseInterval:  "30m",
			JitterPercent: 10,
		},
	}

	client := newMockClient(mock)
	executors := map[string]modules.Executor{}
	factsMap := map[string]any{}

	_, err := RunOnce(context.Background(), client, executors, factsMap, false, "")
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if !mock.reportCalled {
		t.Error("RunOnce did not call ReportResult, expected call")
	}
}

func TestRunOnce_CheckInError(t *testing.T) {
	mock := &mockAgentClient{
		checkInErr: errors.New("connection refused"),
	}

	client := newMockClient(mock)
	_, err := RunOnce(context.Background(), client, nil, nil, false, "")
	if err == nil {
		t.Fatal("RunOnce expected error on check-in failure")
	}
}

func TestRunOnce_ParsesServerTiming(t *testing.T) {
	mock := &mockAgentClient{
		checkInResp: &pb.CheckInResponse{
			Modules:       nil,
			BaseInterval:  "5m",
			JitterPercent: 20,
		},
	}

	client := newMockClient(mock)
	result, err := RunOnce(context.Background(), client, map[string]modules.Executor{}, nil, true, "")
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if result.BaseInterval != 5*time.Minute {
		t.Errorf("BaseInterval = %v, want 5m", result.BaseInterval)
	}
	if result.JitterPct != 20 {
		t.Errorf("JitterPct = %d, want 20", result.JitterPct)
	}
}

func TestRunOnce_DefaultTiming(t *testing.T) {
	mock := &mockAgentClient{
		checkInResp: &pb.CheckInResponse{
			Modules: nil,
			// No timing info -- use defaults.
		},
	}

	client := newMockClient(mock)
	result, err := RunOnce(context.Background(), client, map[string]modules.Executor{}, nil, true, "")
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if result.BaseInterval != 30*time.Minute {
		t.Errorf("BaseInterval = %v, want 30m (default)", result.BaseInterval)
	}
	if result.JitterPct != 10 {
		t.Errorf("JitterPct = %d, want 10 (default)", result.JitterPct)
	}
}

func TestLoop_BackoffOnError_DoesNotCrash(t *testing.T) {
	// Create a mock that always fails CheckIn.
	callCount := 0
	mock := &mockAgentClient{
		checkInErr: errors.New("server unavailable"),
	}

	// Use a mock client that counts calls.
	client := newMockClient(mock)

	// Cancel after a short period to avoid infinite loop.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Track that we actually tried multiple times by using a wrapper.
	origAgent := client.agent
	client.agent = &countingMockAgent{
		inner:     origAgent,
		callCount: &callCount,
	}

	executors := map[string]modules.Executor{}

	// Loop should return context.DeadlineExceeded, not panic.
	err := Loop(ctx, client, executors, nil)
	if err == nil {
		t.Fatal("Loop returned nil, expected context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Loop error = %v, want context.DeadlineExceeded", err)
	}

	// Verify we attempted at least one iteration (fact reporting calls CheckIn indirectly).
	if callCount < 1 {
		t.Errorf("Loop made %d check-in attempts, expected >= 1", callCount)
	}
}

// countingMockAgent wraps a mock to count CheckIn calls.
type countingMockAgent struct {
	inner     pb.IsgovAgentClient
	callCount *int
}

func (c *countingMockAgent) Register(ctx context.Context, req *pb.RegisterRequest, opts ...grpc.CallOption) (*pb.RegisterResponse, error) {
	return c.inner.Register(ctx, req, opts...)
}

func (c *countingMockAgent) CheckIn(ctx context.Context, req *pb.CheckInRequest, opts ...grpc.CallOption) (*pb.CheckInResponse, error) {
	*c.callCount++
	return c.inner.CheckIn(ctx, req, opts...)
}

func (c *countingMockAgent) ReportResult(ctx context.Context, req *pb.ReportResultRequest, opts ...grpc.CallOption) (*pb.ReportResultResponse, error) {
	return c.inner.ReportResult(ctx, req, opts...)
}

func (c *countingMockAgent) ReportFacts(ctx context.Context, req *pb.ReportFactsRequest, opts ...grpc.CallOption) (*pb.ReportFactsResponse, error) {
	return c.inner.ReportFacts(ctx, req, opts...)
}
