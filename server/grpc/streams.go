// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
)

// streamManager tracks connected agent streams for push notifications.
// When config changes, it pushes new policy bundles to all connected agents.
// When a force-push is requested, it signals the target agent's stream.
type streamManager struct {
	mu               sync.RWMutex
	streams          map[string]*agentStream        // envoy_id → stream
	sendBufferSize   int                            // per-agent send channel capacity
	pending          map[string][]*pendingPromotion // envoy_id → queued messages
	fleetIndex       *fleet.FleetIndex
	metrics          *metrics.Metrics // per-instance prometheus collectors
	promotionTimeout time.Duration    // default: 12 minutes
	maxConns         int              // 0 → no cap; >0 → refuse new envoys past this held-stream count
	// onDisconnect, when set, runs after the envoy's current stream leaves the
	// map (its own cleanup or closeStream) — not when a reconnect replaced it.
	// The task router uses it to fail targets the dropped stream was carrying.
	onDisconnect func(envoyID string)
}

type agentStream struct {
	envoyID   string
	hostname  string
	send      chan *pb.ServerStreamMessage
	done      chan struct{}
	promoted  bool  // true if opened via stream_requested (not always-on)
	workCount int32 // pending dispatches; StreamRelease sent when 0 and promoted
}

// pendingPromotion holds a message queued for delivery when an agent opens a stream.
type pendingPromotion struct {
	msg       *pb.ServerStreamMessage
	onTimeout func() // called if stream never opens
	timer     *time.Timer
}

func newStreamManager(sendBuf int, fleetIndex *fleet.FleetIndex, m *metrics.Metrics, promotionTimeout time.Duration, maxConns int) *streamManager {
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
		metrics:          m,
		promotionTimeout: promotionTimeout,
		maxConns:         maxConns,
	}
}

// register adds an agent stream. Returns the stream, a cleanup function, and
// whether the stream was admitted. admitted is false only when the held-stream
// cap (checkin.max_connections) is reached — the caller must then return
// ResourceExhausted and not use the stream or call cleanup. The cap is enforced
// under the same write lock that mutates the map, so the count can't race past
// it; a reconnecting envoy (already in the map) replaces its own slot and never
// counts against the cap.
func (sm *streamManager) register(envoyID, hostname string) (*agentStream, func(), bool) {
	as := &agentStream{
		envoyID:  envoyID,
		hostname: hostname,
		send:     make(chan *pb.ServerStreamMessage, sm.sendBufferSize),
		done:     make(chan struct{}),
	}

	sm.mu.Lock()
	// If there's an existing stream for this envoy, close it.
	old, reconnect := sm.streams[envoyID]
	// Held-stream cap: refuse a genuinely new envoy past the limit so the
	// server sheds (ResourceExhausted → agent backoff) instead of climbing to
	// OOM. Reconnects don't count — they reuse their own slot.
	if sm.maxConns > 0 && !reconnect && len(sm.streams) >= sm.maxConns {
		sm.mu.Unlock()
		sm.metrics.CheckinsTotal.WithLabelValues("stream_shed").Inc()
		return nil, func() {}, false
	}
	if reconnect {
		close(old.done)
	}
	sm.streams[envoyID] = as

	// Deliver any pending promotion messages.
	if pending, ok := sm.pending[envoyID]; ok && len(pending) > 0 {
		as.promoted = true
		as.workCount = int32(len(pending))
		for _, p := range pending {
			p.timer.Stop()
			select {
			case as.send <- p.msg:
			default:
				slog.Warn("promotion dispatch dropped (buffer full)", "envoy_id", envoyID)
				atomic.AddInt32(&as.workCount, -1)
			}
		}
		delete(sm.pending, envoyID)
		if sm.fleetIndex != nil {
			sm.fleetIndex.ClearStreamRequested(envoyID)
		}
		slog.Info("delivered pending promotion messages",
			"envoy_id", envoyID, "count", len(pending))
	}
	active, promoted := len(sm.streams), as.promoted
	sm.mu.Unlock()

	sm.metrics.StreamsActive.Set(float64(active))
	slog.Info("agent stream connected", "envoy_id", envoyID, "hostname", hostname,
		"promoted", promoted)

	return as, func() {
		sm.mu.Lock()
		// Only remove if it's still our stream (could have been replaced, or
		// already removed by closeStream).
		current := sm.streams[envoyID] == as
		if current {
			delete(sm.streams, envoyID)
		}
		active := len(sm.streams)
		sm.mu.Unlock()
		sm.metrics.StreamsActive.Set(float64(active))
		if current && sm.onDisconnect != nil {
			sm.onDisconnect(envoyID)
		}
		slog.Info("agent stream disconnected", "envoy_id", envoyID, "hostname", hostname)
	}, true
}

// closeStream ends the envoy's held stream, if any: it is removed from the map
// and its done channel closed, so the writer goroutine returns, the AgentStream
// handler exits and the connection's stream context is cancelled. Used on
// revoke and delete, so pushes stop reaching the envoy at once rather than when
// it next sends a message. Returns whether a stream was closed. The stream's
// own cleanup then finds itself already gone and does nothing.
func (sm *streamManager) closeStream(envoyID string) bool {
	sm.mu.Lock()
	as, ok := sm.streams[envoyID]
	if ok {
		delete(sm.streams, envoyID)
		close(as.done)
	}
	active := len(sm.streams)
	sm.mu.Unlock()
	if !ok {
		return false
	}
	sm.metrics.StreamsActive.Set(float64(active))
	if sm.onDisconnect != nil {
		sm.onDisconnect(envoyID)
	}
	slog.Info("agent stream closed by server", "envoy_id", envoyID)
	return true
}

// queueForPromotion stores a message for delivery when the agent opens a stream.
// Sets StreamRequested in FleetIndex so the next unary check-in includes the flag.
// If the stream is not opened within the promotion timeout, onTimeout is called.
func (sm *streamManager) queueForPromotion(envoyID string, msg *pb.ServerStreamMessage, onTimeout func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// If already connected, deliver immediately.
	if as, ok := sm.streams[envoyID]; ok {
		select {
		case as.send <- msg:
			atomic.AddInt32(&as.workCount, 1)
			if !as.promoted {
				as.promoted = true
			}
		default:
			slog.Warn("promotion dispatch dropped (buffer full, stream connected)", "envoy_id", envoyID)
			if onTimeout != nil {
				daemon.SafeGo("promotion-timeout", onTimeout)
			}
		}
		return
	}

	timer := time.AfterFunc(sm.promotionTimeout, func() {
		defer daemon.Recover("promotion-timeout-timer", nil)
		sm.mu.Lock()
		// Remove this specific pending entry.
		pending := sm.pending[envoyID]
		remaining := pending[:0]
		for _, p := range pending {
			if p.msg != msg {
				remaining = append(remaining, p)
			}
		}
		if len(remaining) == 0 {
			delete(sm.pending, envoyID)
		} else {
			sm.pending[envoyID] = remaining
		}
		// Clear fleet flag if no more pending.
		if len(sm.pending[envoyID]) == 0 && sm.fleetIndex != nil {
			sm.fleetIndex.ClearStreamRequested(envoyID)
		}
		sm.mu.Unlock()

		slog.Warn("stream promotion timed out", "envoy_id", envoyID)
		if onTimeout != nil {
			onTimeout()
		}
	})

	sm.pending[envoyID] = append(sm.pending[envoyID], &pendingPromotion{
		msg:       msg,
		onTimeout: onTimeout,
		timer:     timer,
	})

	if sm.fleetIndex != nil {
		sm.fleetIndex.SetStreamRequested(envoyID, true)
	}
}

// workCompleted decrements the work counter on a promoted stream.
// When the counter reaches zero, sends a StreamRelease message.
func (sm *streamManager) workCompleted(envoyID string) {
	sm.mu.RLock()
	as, ok := sm.streams[envoyID]
	promoted := ok && as.promoted // promoted is written under sm.mu
	sm.mu.RUnlock()
	if !promoted {
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
			slog.Info("sent stream release", "envoy_id", envoyID)
		default:
			slog.Warn("stream release dropped (buffer full)", "envoy_id", envoyID)
		}
	}
}

// forcePush sends a force-push signal to a specific agent. If envoyID is empty,
// sends to all connected agents.
func (sm *streamManager) forcePush(envoyID string) int {
	traceparent, _ := GenerateTraceparent()
	msg := &pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_ForcePush{
			ForcePush: &pb.StreamForcePush{Traceparent: traceparent},
		},
	}

	if envoyID != "" {
		sm.mu.RLock()
		as, ok := sm.streams[envoyID]
		sm.mu.RUnlock()
		if !ok {
			return 0
		}
		select {
		case as.send <- msg:
			sm.metrics.ForcePushesTotal.WithLabelValues("single").Inc()
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
	if count > 0 {
		sm.metrics.ForcePushesTotal.WithLabelValues("all").Add(float64(count))
	}
	return count
}

// isConnected returns true if the envoy has an active stream.
func (sm *streamManager) isConnected(envoyID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.streams[envoyID]
	return ok
}

// connectedEnvoyIDs returns the set of currently connected envoy IDs.
func (sm *streamManager) connectedEnvoyIDs() map[string]bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	ids := make(map[string]bool, len(sm.streams))
	for id := range sm.streams {
		ids[id] = true
	}
	return ids
}

// dispatchPush sends a TaskDispatch to a specific agent. The send is
// non-blocking: a full send buffer drops the dispatch (logged) rather than
// stalling the dispatcher. Returns false if the agent is not connected or the
// buffer is full, and the caller marks the target offline — never a silent drop.
func (sm *streamManager) dispatchPush(envoyID string, msg *pb.ServerStreamMessage) bool {
	sm.mu.RLock()
	as, ok := sm.streams[envoyID]
	sm.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case as.send <- msg:
		return true
	default:
		slog.Warn("task dispatch dropped (buffer full)", "envoy_id", envoyID)
		return false
	}
}
