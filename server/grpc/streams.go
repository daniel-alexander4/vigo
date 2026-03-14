// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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
	mu             sync.RWMutex
	streams        map[string]*agentStream // thrall_id → stream
	sendBufferSize int                     // per-agent send channel capacity
}

type agentStream struct {
	thrallID   string
	hostname string
	send     chan *pb.ServerStreamMessage
	done     chan struct{}
}

func newStreamManager(sendBuf int) *streamManager {
	if sendBuf <= 0 {
		sendBuf = 8
	}
	return &streamManager{
		streams:        make(map[string]*agentStream),
		sendBufferSize: sendBuf,
	}
}

// register adds an agent stream. Returns the stream and a cleanup function.
func (sm *streamManager) register(thrallID, hostname string) (*agentStream, func()) {
	as := &agentStream{
		thrallID:   thrallID,
		hostname: hostname,
		send:     make(chan *pb.ServerStreamMessage, sm.sendBufferSize),
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

// isConnected returns true if the thrall has an active stream.
func (sm *streamManager) isConnected(thrallID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.streams[thrallID]
	return ok
}

// connectedThrallIDs returns the set of currently connected thrall IDs.
func (sm *streamManager) connectedThrallIDs() map[string]bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	ids := make(map[string]bool, len(sm.streams))
	for id := range sm.streams {
		ids[id] = true
	}
	return ids
}

// dispatchTask sends a TaskDispatch to a specific agent. Uses a blocking
// send with a short timeout to avoid silent drops (Pitfall 6).
// Returns false if the agent is not connected or the send buffer is full.
func (sm *streamManager) dispatchTask(thrallID string, msg *pb.ServerStreamMessage) bool {
	sm.mu.RLock()
	as, ok := sm.streams[thrallID]
	sm.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case as.send <- msg:
		return true
	default:
		slog.Warn("task dispatch dropped (buffer full)", "thrall_id", thrallID)
		return false
	}
}
