package checkin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// StreamConn wraps a persistent bidirectional gRPC stream to the server.
// The server pushes policy changes and force-push signals asynchronously;
// the agent sends check-in, report-result, and report-traits messages.
type StreamConn struct {
	stream pb.HobnobAgent_AgentStreamClient
	cancel context.CancelFunc

	// Server-initiated push channels.
	PolicyPush chan *pb.StreamPolicyPush
	ForcePush  chan struct{}

	// Response channels for request-response correlation.
	checkInResp     chan *pb.CheckInResponse
	reportResultAck chan struct{}

	done chan struct{} // closed when readLoop exits
	mu   sync.Mutex   // guards stream.Send
}

// OpenStream opens a bidirectional gRPC stream and performs the initial
// handshake (first message must be a CheckInRequest). Returns the stream
// connection and the server's initial CheckInResponse.
func OpenStream(ctx context.Context, client *Client, req *pb.CheckInRequest) (*StreamConn, *pb.CheckInResponse, error) {
	streamCtx, cancel := context.WithCancel(ctx)

	rawStream, err := client.agent.AgentStream(streamCtx)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("open stream: %w", err)
	}

	// Send handshake.
	if err := rawStream.Send(&pb.AgentStreamMessage{
		Msg: &pb.AgentStreamMessage_CheckIn{CheckIn: req},
	}); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("send handshake: %w", err)
	}

	// Wait for initial response.
	msg, err := rawStream.Recv()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("recv handshake: %w", err)
	}
	resp := msg.GetCheckInResponse()
	if resp == nil {
		cancel()
		return nil, nil, fmt.Errorf("expected check_in_response, got %T", msg.GetMsg())
	}

	sc := &StreamConn{
		stream:          rawStream,
		cancel:          cancel,
		PolicyPush:      make(chan *pb.StreamPolicyPush, 4),
		ForcePush:       make(chan struct{}, 4),
		checkInResp:     make(chan *pb.CheckInResponse, 1),
		reportResultAck: make(chan struct{}, 4),
		done:            make(chan struct{}),
	}
	go sc.readLoop()

	return sc, resp, nil
}

// readLoop reads server messages and dispatches them to channels.
func (sc *StreamConn) readLoop() {
	defer close(sc.done)
	for {
		msg, err := sc.stream.Recv()
		if err != nil {
			if err != io.EOF {
				slog.Warn("stream read error", "error", err)
			}
			return
		}

		switch m := msg.GetMsg().(type) {
		case *pb.ServerStreamMessage_CheckInResponse:
			select {
			case sc.checkInResp <- m.CheckInResponse:
			default:
			}
		case *pb.ServerStreamMessage_ReportResultAck:
			select {
			case sc.reportResultAck <- struct{}{}:
			default:
			}
		case *pb.ServerStreamMessage_PolicyPush:
			select {
			case sc.PolicyPush <- m.PolicyPush:
			default:
				slog.Warn("policy push dropped (buffer full)")
			}
		case *pb.ServerStreamMessage_ForcePush:
			select {
			case sc.ForcePush <- struct{}{}:
			default:
			}
		case *pb.ServerStreamMessage_ReportTraitsAck:
			// Ignored — traits are delivered inline with check-in.
		}
	}
}

// CheckIn sends a check-in on the stream and waits for the response.
func (sc *StreamConn) CheckIn(ctx context.Context, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	sc.mu.Lock()
	err := sc.stream.Send(&pb.AgentStreamMessage{
		Msg: &pb.AgentStreamMessage_CheckIn{CheckIn: req},
	})
	sc.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send check-in: %w", err)
	}

	select {
	case resp := <-sc.checkInResp:
		return resp, nil
	case <-sc.done:
		return nil, fmt.Errorf("stream closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ReportResult sends run results on the stream. Fire-and-forget — the ack
// is drained by readLoop but not waited on.
func (sc *StreamConn) ReportResult(req *pb.ReportResultRequest) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.stream.Send(&pb.AgentStreamMessage{
		Msg: &pb.AgentStreamMessage_ReportResult{ReportResult: req},
	})
}

// Done returns a channel that is closed when the stream disconnects.
func (sc *StreamConn) Done() <-chan struct{} {
	return sc.done
}

// Close shuts down the stream connection.
func (sc *StreamConn) Close() {
	sc.cancel()
}
