package grpc

import (
	"log/slog"
	"sync"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// streamManager tracks connected agent streams for push notifications.
// When config changes, it pushes new policy bundles to all connected agents.
// When a force-push is requested, it signals the target agent's stream.
type streamManager struct {
	mu      sync.RWMutex
	streams map[string]*agentStream // thrall_id → stream
}

type agentStream struct {
	thrallID   string
	hostname string
	send     chan *pb.ServerStreamMessage
	done     chan struct{}
}

func newStreamManager() *streamManager {
	return &streamManager{
		streams: make(map[string]*agentStream),
	}
}

// register adds an agent stream. Returns the stream and a cleanup function.
func (sm *streamManager) register(thrallID, hostname string) (*agentStream, func()) {
	as := &agentStream{
		thrallID:   thrallID,
		hostname: hostname,
		send:     make(chan *pb.ServerStreamMessage, 8),
		done:     make(chan struct{}),
	}

	sm.mu.Lock()
	// If there's an existing stream for this thrall, close it.
	if old, ok := sm.streams[thrallID]; ok {
		close(old.done)
	}
	sm.streams[thrallID] = as
	sm.mu.Unlock()

	slog.Info("agent stream connected", "thrall_id", thrallID, "hostname", hostname)

	return as, func() {
		sm.mu.Lock()
		// Only remove if it's still our stream (could have been replaced).
		if sm.streams[thrallID] == as {
			delete(sm.streams, thrallID)
		}
		sm.mu.Unlock()
		slog.Info("agent stream disconnected", "thrall_id", thrallID, "hostname", hostname)
	}
}

// pushPolicy sends a policy update to a specific agent. Non-blocking — if the
// agent's send buffer is full, the message is dropped (agent will get it on
// next check-in within the stream).
func (sm *streamManager) pushPolicy(thrallID string, msg *pb.ServerStreamMessage) {
	sm.mu.RLock()
	as, ok := sm.streams[thrallID]
	sm.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case as.send <- msg:
	default:
		slog.Warn("stream send buffer full, dropping policy push", "thrall_id", thrallID)
	}
}

// pushPolicyAll sends a policy update to all connected agents. Non-blocking.
func (sm *streamManager) pushPolicyAll(msg *pb.ServerStreamMessage) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, as := range sm.streams {
		select {
		case as.send <- msg:
		default:
			slog.Warn("stream send buffer full, dropping policy push", "thrall_id", as.thrallID)
		}
	}
}

// forcePush sends a force-push signal to a specific agent. If thrallID is empty,
// sends to all connected agents.
func (sm *streamManager) forcePush(thrallID string) int {
	msg := &pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_ForcePush{
			ForcePush: &pb.StreamForcePush{},
		},
	}

	if thrallID != "" {
		sm.mu.RLock()
		as, ok := sm.streams[thrallID]
		sm.mu.RUnlock()
		if !ok {
			return 0
		}
		select {
		case as.send <- msg:
			return 1
		default:
			return 0
		}
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()
	count := 0
	for _, as := range sm.streams {
		select {
		case as.send <- msg:
			count++
		default:
		}
	}
	return count
}

// connectedCount returns the number of connected agent streams.
func (sm *streamManager) connectedCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.streams)
}
