// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/fleet"
)

// streamManager tracks connected agent streams for push notifications.
// When config changes, it pushes new policy bundles to all connected agents.
// When a force-push is requested, it signals the target agent's stream.
type streamManager struct {
	mu               sync.RWMutex
	streams          map[string]*agentStream      // thrall_id → stream
	sendBufferSize   int                          // per-agent send channel capacity
	pending          map[string][]*pendingPromotion // thrall_id → queued messages
	fleetIndex       *fleet.FleetIndex
	promotionTimeout time.Duration // default: 12 minutes
}

type agentStream struct {
	thrallID  string
	hostname  string
	send      chan *pb.ServerStreamMessage
	done      chan struct{}
	promoted  bool  // true if opened via stream_requested (not always-on)
	workCount int32 // pending dispatches; StreamRelease sent when 0 and promoted
}

// pendingPromotion holds a message queued for delivery when an agent opens a stream.
type pendingPromotion struct {
	msg       *pb.ServerStreamMessage
	onTimeout func()      // called if stream never opens
	timer     *time.Timer
}

func newStreamManager(sendBuf int, fleetIndex *fleet.FleetIndex, promotionTimeout time.Duration) *streamManager {
	if sendBuf <= 0 {
		sendBuf = 8
	}
	if promotionTimeout <= 0 {
		promotionTimeout = 12 * time.Minute
	}
	return &streamManager{
		streams:          make(map[string]*agentStream),
		sendBufferSize:   sendBuf,
		pending:          make(map[string][]*pendingPromotion),
		fleetIndex:       fleetIndex,
		promotionTimeout: promotionTimeout,
	}
}

// register adds an agent stream. Returns the stream and a cleanup function.
func (sm *streamManager) register(thrallID, hostname string) (*agentStream, func()) {
	as := &agentStream{
		thrallID: thrallID,
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

	// Deliver any pending promotion messages.
	if pending, ok := sm.pending[thrallID]; ok && len(pending) > 0 {
		as.promoted = true
		as.workCount = int32(len(pending))
		for _, p := range pending {
			p.timer.Stop()
			select {
			case as.send <- p.msg:
			default:
				slog.Warn("promotion dispatch dropped (buffer full)", "thrall_id", thrallID)
				atomic.AddInt32(&as.workCount, -1)
			}
		}
		delete(sm.pending, thrallID)
		if sm.fleetIndex != nil {
			sm.fleetIndex.ClearStreamRequested(thrallID)
		}
		slog.Info("delivered pending promotion messages",
			"thrall_id", thrallID, "count", len(pending))
	}
	sm.mu.Unlock()

	slog.Info("agent stream connected", "thrall_id", thrallID, "hostname", hostname,
		"promoted", as.promoted)

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

// queueForPromotion stores a message for delivery when the agent opens a stream.
// Sets StreamRequested in FleetIndex so the next unary check-in includes the flag.
// If the stream is not opened within the promotion timeout, onTimeout is called.
func (sm *streamManager) queueForPromotion(thrallID string, msg *pb.ServerStreamMessage, onTimeout func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// If already connected, deliver immediately.
	if as, ok := sm.streams[thrallID]; ok {
		select {
		case as.send <- msg:
			atomic.AddInt32(&as.workCount, 1)
			if !as.promoted {
				as.promoted = true
			}
		default:
			slog.Warn("promotion dispatch dropped (buffer full, stream connected)", "thrall_id", thrallID)
			if onTimeout != nil {
				go onTimeout()
			}
		}
		return
	}

	timer := time.AfterFunc(sm.promotionTimeout, func() {
		sm.mu.Lock()
		// Remove this specific pending entry.
		pending := sm.pending[thrallID]
		remaining := pending[:0]
		for _, p := range pending {
			if p.msg != msg {
				remaining = append(remaining, p)
			}
		}
		if len(remaining) == 0 {
			delete(sm.pending, thrallID)
		} else {
			sm.pending[thrallID] = remaining
		}
		// Clear fleet flag if no more pending.
		if len(sm.pending[thrallID]) == 0 && sm.fleetIndex != nil {
			sm.fleetIndex.ClearStreamRequested(thrallID)
		}
		sm.mu.Unlock()

		slog.Warn("stream promotion timed out", "thrall_id", thrallID)
		if onTimeout != nil {
			onTimeout()
		}
	})

	sm.pending[thrallID] = append(sm.pending[thrallID], &pendingPromotion{
		msg:       msg,
		onTimeout: onTimeout,
		timer:     timer,
	})

	if sm.fleetIndex != nil {
		sm.fleetIndex.SetStreamRequested(thrallID, true)
	}
}

// workCompleted decrements the work counter on a promoted stream.
// When the counter reaches zero, sends a StreamRelease message.
func (sm *streamManager) workCompleted(thrallID string) {
	sm.mu.RLock()
	as, ok := sm.streams[thrallID]
	sm.mu.RUnlock()
	if !ok || !as.promoted {
		return
	}

	newCount := atomic.AddInt32(&as.workCount, -1)
	if newCount <= 0 {
		msg := &pb.ServerStreamMessage{
			Msg: &pb.ServerStreamMessage_StreamRelease{
				StreamRelease: &pb.StreamRelease{
					Reason: "all dispatched work complete",
				},
			},
		}
		select {
		case as.send <- msg:
			slog.Info("sent stream release", "thrall_id", thrallID)
		default:
			slog.Warn("stream release dropped (buffer full)", "thrall_id", thrallID)
		}
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
