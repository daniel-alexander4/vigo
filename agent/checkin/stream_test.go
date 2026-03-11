package checkin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hobnob/hobnob/agent/modules"
	"github.com/hobnob/hobnob/agent/traits"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc"
)

// mockBidiStream implements pb.HobnobAgent_AgentStreamClient for testing.
type mockBidiStream struct {
	grpc.ClientStream
	mu       sync.Mutex
	sent     []*pb.AgentStreamMessage
	recvMsgs []*pb.ServerStreamMessage
	recvIdx  int
	recvCh   chan *pb.ServerStreamMessage // if set, Recv blocks on this
	recvErr  error                        // returned after recvMsgs are exhausted
}

func (m *mockBidiStream) Send(msg *pb.AgentStreamMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockBidiStream) Recv() (*pb.ServerStreamMessage, error) {
	if m.recvCh != nil {
		msg, ok := <-m.recvCh
		if !ok {
			if m.recvErr != nil {
				return nil, m.recvErr
			}
			return nil, errors.New("stream closed")
		}
		return msg, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recvIdx < len(m.recvMsgs) {
		msg := m.recvMsgs[m.recvIdx]
		m.recvIdx++
		return msg, nil
	}
	if m.recvErr != nil {
		return nil, m.recvErr
	}
	return nil, errors.New("no more messages")
}

func (m *mockBidiStream) CloseSend() error { return nil }

// mockStreamAgentClient returns a mock that provides the bidi stream.
type mockStreamAgentClient struct {
	mockAgentClient
	stream    *mockBidiStream
	streamErr error
}

func (m *mockStreamAgentClient) AgentStream(_ context.Context, _ ...grpc.CallOption) (pb.HobnobAgent_AgentStreamClient, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	return m.stream, nil
}

func TestOpenStream_Handshake(t *testing.T) {
	initialResp := &pb.CheckInResponse{
		PolicyVersion: 42,
		BaseInterval:  "5m",
		JitterPercent: 10,
		PolicyBundle: &pb.PolicyBundle{
			PolicyVersion: 42,
		},
	}

	stream := &mockBidiStream{
		recvMsgs: []*pb.ServerStreamMessage{
			{Msg: &pb.ServerStreamMessage_CheckInResponse{CheckInResponse: initialResp}},
		},
		recvErr: errors.New("stream closed"), // readLoop will get this after handshake
	}

	mock := &mockStreamAgentClient{stream: stream}
	client := &Client{
		agent:      mock,
		thrallID:     "test-thrall",
		privateKey: make([]byte, 64),
	}

	req := client.BuildCheckInRequest("", nil, 0, nil)
	sc, resp, err := OpenStream(context.Background(), client, req)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer sc.Close()

	if resp.PolicyVersion != 42 {
		t.Errorf("PolicyVersion = %d, want 42", resp.PolicyVersion)
	}

	// Verify handshake was sent.
	if len(stream.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(stream.sent))
	}
	if stream.sent[0].GetCheckIn() == nil {
		t.Error("first sent message should be check_in")
	}
	if stream.sent[0].GetCheckIn().GetThrallId() != "test-thrall" {
		t.Errorf("thrall_id = %q, want %q", stream.sent[0].GetCheckIn().GetThrallId(), "test-thrall")
	}
}

func TestOpenStream_StreamError(t *testing.T) {
	mock := &mockStreamAgentClient{
		streamErr: errors.New("server does not support streaming"),
	}
	client := &Client{
		agent:      mock,
		thrallID:     "test-thrall",
		privateKey: make([]byte, 64),
	}

	req := client.BuildCheckInRequest("", nil, 0, nil)
	_, _, err := OpenStream(context.Background(), client, req)
	if err == nil {
		t.Fatal("expected error when stream is unavailable")
	}
}

func TestStreamConn_PolicyPush(t *testing.T) {
	recvCh := make(chan *pb.ServerStreamMessage, 4)

	stream := &mockBidiStream{recvCh: recvCh}

	// Simulate handshake already done — create StreamConn directly.
	sc := &StreamConn{
		stream:          stream,
		cancel:          func() {},
		PolicyPush:      make(chan *pb.StreamPolicyPush, 4),
		ForcePush:       make(chan struct{}, 4),
		checkInResp:     make(chan *pb.CheckInResponse, 1),
		reportResultAck: make(chan struct{}, 4),
		done:            make(chan struct{}),
	}
	go sc.readLoop()

	// Send a policy push.
	pushBundle := &pb.PolicyBundle{PolicyVersion: 99}
	recvCh <- &pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_PolicyPush{
			PolicyPush: &pb.StreamPolicyPush{
				PolicyBundle:  pushBundle,
				PolicyVersion: 99,
			},
		},
	}

	select {
	case push := <-sc.PolicyPush:
		if push.PolicyVersion != 99 {
			t.Errorf("PolicyVersion = %d, want 99", push.PolicyVersion)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy push")
	}

	close(recvCh)
	<-sc.Done()
}

func TestStreamConn_ForcePush(t *testing.T) {
	recvCh := make(chan *pb.ServerStreamMessage, 4)

	stream := &mockBidiStream{recvCh: recvCh}

	sc := &StreamConn{
		stream:          stream,
		cancel:          func() {},
		PolicyPush:      make(chan *pb.StreamPolicyPush, 4),
		ForcePush:       make(chan struct{}, 4),
		checkInResp:     make(chan *pb.CheckInResponse, 1),
		reportResultAck: make(chan struct{}, 4),
		done:            make(chan struct{}),
	}
	go sc.readLoop()

	recvCh <- &pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_ForcePush{
			ForcePush: &pb.StreamForcePush{},
		},
	}

	select {
	case <-sc.ForcePush:
		// OK
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for force push")
	}

	close(recvCh)
	<-sc.Done()
}

func TestStreamConn_CheckIn(t *testing.T) {
	recvCh := make(chan *pb.ServerStreamMessage, 4)

	stream := &mockBidiStream{recvCh: recvCh}

	sc := &StreamConn{
		stream:          stream,
		cancel:          func() {},
		PolicyPush:      make(chan *pb.StreamPolicyPush, 4),
		ForcePush:       make(chan struct{}, 4),
		checkInResp:     make(chan *pb.CheckInResponse, 1),
		reportResultAck: make(chan struct{}, 4),
		done:            make(chan struct{}),
	}
	go sc.readLoop()

	// Send check-in response that the readLoop will dispatch.
	go func() {
		time.Sleep(10 * time.Millisecond)
		recvCh <- &pb.ServerStreamMessage{
			Msg: &pb.ServerStreamMessage_CheckInResponse{
				CheckInResponse: &pb.CheckInResponse{
					PolicyVersion: 77,
					NoChange:      true,
				},
			},
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := sc.CheckIn(ctx, &pb.CheckInRequest{ThrallId: "test"})
	if err != nil {
		t.Fatalf("CheckIn: %v", err)
	}
	if resp.PolicyVersion != 77 {
		t.Errorf("PolicyVersion = %d, want 77", resp.PolicyVersion)
	}
	if !resp.NoChange {
		t.Error("expected NoChange=true")
	}

	// Verify request was sent.
	stream.mu.Lock()
	if len(stream.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(stream.sent))
	}
	if stream.sent[0].GetCheckIn() == nil {
		t.Error("sent message should be check_in")
	}
	stream.mu.Unlock()

	close(recvCh)
	<-sc.Done()
}

func TestStreamConn_ReportResult(t *testing.T) {
	recvCh := make(chan *pb.ServerStreamMessage, 4)
	stream := &mockBidiStream{recvCh: recvCh}

	sc := &StreamConn{
		stream:          stream,
		cancel:          func() {},
		PolicyPush:      make(chan *pb.StreamPolicyPush, 4),
		ForcePush:       make(chan struct{}, 4),
		checkInResp:     make(chan *pb.CheckInResponse, 1),
		reportResultAck: make(chan struct{}, 4),
		done:            make(chan struct{}),
	}
	go sc.readLoop()

	err := sc.ReportResult(&pb.ReportResultRequest{
		RunId:  "run-1",
		ThrallId: "thrall-1",
		Results: []*pb.ModuleResult{
			{ModuleName: "nginx", ActionTaken: "updated", Changed: true},
		},
	})
	if err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	stream.mu.Lock()
	if len(stream.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(stream.sent))
	}
	if stream.sent[0].GetReportResult() == nil {
		t.Error("sent message should be report_result")
	}
	if stream.sent[0].GetReportResult().GetRunId() != "run-1" {
		t.Errorf("RunId = %q, want %q", stream.sent[0].GetReportResult().GetRunId(), "run-1")
	}
	stream.mu.Unlock()

	close(recvCh)
	<-sc.Done()
}

func TestStreamConn_DoneOnDisconnect(t *testing.T) {
	recvCh := make(chan *pb.ServerStreamMessage)
	stream := &mockBidiStream{recvCh: recvCh}

	sc := &StreamConn{
		stream:          stream,
		cancel:          func() {},
		PolicyPush:      make(chan *pb.StreamPolicyPush, 4),
		ForcePush:       make(chan struct{}, 4),
		checkInResp:     make(chan *pb.CheckInResponse, 1),
		reportResultAck: make(chan struct{}, 4),
		done:            make(chan struct{}),
	}
	go sc.readLoop()

	// Close the receive channel to simulate disconnect.
	close(recvCh)

	select {
	case <-sc.Done():
		// OK — stream detected disconnect.
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Done()")
	}
}

func TestConverge_NoChange_NoCachedBundle(t *testing.T) {
	resp := &pb.CheckInResponse{
		NoChange:      true,
		PolicyVersion: 10,
		BaseInterval:  "3m",
		JitterPercent: 15,
	}

	rr, err := converge(context.Background(), resp, nil, nil, nil, false, "", nil, nil)
	if err != nil {
		t.Fatalf("converge: %v", err)
	}
	if !rr.NoChange {
		t.Error("expected NoChange=true")
	}
	if rr.PolicyVersion != 10 {
		t.Errorf("PolicyVersion = %d, want 10", rr.PolicyVersion)
	}
	if rr.BaseInterval != 3*time.Minute {
		t.Errorf("BaseInterval = %v, want 3m", rr.BaseInterval)
	}
	if rr.JitterPct != 15 {
		t.Errorf("JitterPct = %d, want 15", rr.JitterPct)
	}
}

func TestConverge_NoChange_UsesCachedBundle(t *testing.T) {
	resp := &pb.CheckInResponse{
		NoChange:      true,
		PolicyVersion: 20,
	}

	ps := &PolicyState{
		Version: 20,
		Bundle: &pb.PolicyBundle{
			PolicyVersion: 20,
			Modules: []*pb.PolicyModule{
				{
					Name: "test-mod",
					Resources: []*pb.PolicyResource{
						{Name: "test-res", Type: "file", Attributes: map[string]string{"path": "/tmp/test"}},
					},
				},
			},
		},
		ModuleCache: NewModuleCache(),
	}

	reportCalled := false
	reportFn := func(_ context.Context, _ string, _ []*pb.ModuleResult) error {
		reportCalled = true
		return nil
	}

	rr, err := converge(context.Background(), resp, reportFn, map[string]modules.Executor{}, map[string]any{}, false, "", nil, ps)
	if err != nil {
		t.Fatalf("converge: %v", err)
	}

	if rr.PolicyVersion != 20 {
		t.Errorf("PolicyVersion = %d, want 20", rr.PolicyVersion)
	}
	if !reportCalled {
		t.Error("expected report to be called (cached bundle has modules)")
	}
}

func TestCollectTraits_NoCustomTraits(t *testing.T) {
	collectors := traits.NewCachedCollectors(10)
	traitsMap, traitsJSON := collectTraits(context.Background(), collectors, nil, "", nil)
	if traitsMap == nil {
		t.Error("traitsMap should not be nil")
	}
	if traitsJSON == nil {
		t.Error("traitsJSON should not be nil (empty map marshals to {})")
	}
}

func TestUpdateState(t *testing.T) {
	ps := &PolicyState{ModuleCache: NewModuleCache()}
	var cachedTraits []*pb.CustomTraitEntry
	var fingerprint string

	rr := &RunResult{
		PolicyVersion:       42,
		PolicyBundle:        &pb.PolicyBundle{PolicyVersion: 42, CustomTraits: []*pb.CustomTraitEntry{{Name: "test", Sha256: "abc"}}},
		ResponseFingerprint: "fp-abc",
	}

	updateState(ps, rr, &cachedTraits, &fingerprint)

	if ps.Version != 42 {
		t.Errorf("Version = %d, want 42", ps.Version)
	}
	if ps.Bundle == nil {
		t.Fatal("Bundle should not be nil")
	}
	if len(cachedTraits) != 1 {
		t.Errorf("cachedTraits len = %d, want 1", len(cachedTraits))
	}
	if fingerprint != "fp-abc" {
		t.Errorf("fingerprint = %q, want %q", fingerprint, "fp-abc")
	}
}

func TestUpdateState_NilFingerprint(t *testing.T) {
	ps := &PolicyState{ModuleCache: NewModuleCache()}
	var cachedTraits []*pb.CustomTraitEntry

	rr := &RunResult{PolicyVersion: 10}
	updateState(ps, rr, &cachedTraits, nil)

	if ps.Version != 10 {
		t.Errorf("Version = %d, want 10", ps.Version)
	}
}
