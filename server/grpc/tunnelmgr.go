// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"github.com/google/uuid"
)

// tunnelManager tracks active Scrier remote access tunnels.
// Each tunnel bridges guacd TCP ↔ agent gRPC TunnelData.
type tunnelManager struct {
	mu           sync.RWMutex
	tunnels      map[string]*activeTunnel          // tunnel_id → tunnel
	agentStreams map[string]chan *activeTunnel       // envoy_id → pending tunnel channel
	guacdAddr    string                             // e.g., "guacd:4822"
	allowedPorts map[uint32]bool
	maxSessions  int
	streamMgr    *streamManager                    // sends TunnelRequest via AgentStream
}

// activeTunnel represents a single remote access session.
type activeTunnel struct {
	tunnelID   string
	envoyID    string
	hostname   string
	protocol   string
	targetPort uint32
	startedAt  time.Time

	// guacd side: TCP connection to guacd process.
	guacdConn net.Conn

	// Agent side: channels bridging to the TunnelStream handler.
	// agentRecv receives bytes from the agent (TCP target → server).
	// agentSend sends bytes to the agent (server → TCP target).
	agentRecv chan []byte
	agentSend chan []byte

	done chan struct{} // closed when tunnel is torn down
}

// NewTunnelManager creates a tunnel manager for Scrier remote access.
func NewTunnelManager(guacdAddr string, allowedPorts []uint32, maxSessions int, sm *streamManager) *tunnelManager {
	ports := make(map[uint32]bool, len(allowedPorts))
	for _, p := range allowedPorts {
		ports[p] = true
	}
	return &tunnelManager{
		tunnels:      make(map[string]*activeTunnel),
		agentStreams: make(map[string]chan *activeTunnel),
		guacdAddr:    guacdAddr,
		allowedPorts: ports,
		maxSessions:  maxSessions,
		streamMgr:    sm,
	}
}

// registerAgentStream registers a TunnelStream from an agent as available
// for tunnel assignment. The returned channel receives an activeTunnel when
// the WebSocket handler opens a tunnel to this envoy.
func (tm *tunnelManager) registerAgentStream(envoyID string, ch chan *activeTunnel) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.agentStreams[envoyID] = ch
}

// unregisterAgentStream removes a pending agent stream registration.
func (tm *tunnelManager) unregisterAgentStream(envoyID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.agentStreams, envoyID)
}

// assignTunnel sends an opened tunnel to the waiting agent stream handler.
// Returns false if no agent stream is registered for this envoy.
func (tm *tunnelManager) assignTunnel(envoyID string, tunnel *activeTunnel) bool {
	tm.mu.RLock()
	ch, ok := tm.agentStreams[envoyID]
	tm.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case ch <- tunnel:
		return true
	default:
		return false
	}
}

// hasAgentStream checks if an agent has a TunnelStream open and waiting.
func (tm *tunnelManager) hasAgentStream(envoyID string) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	_, ok := tm.agentStreams[envoyID]
	return ok
}

// openTunnel is now replaced by the exported OpenTunnel which creates a
// local TCP proxy instead of connecting directly to guacd.

// bridgeGuacdToAgent runs two goroutines that relay bytes between guacd TCP
// and the agent channels.
func (tm *tunnelManager) bridgeGuacdToAgent(t *activeTunnel) {
	var wg sync.WaitGroup
	wg.Add(2)

	// guacd → agent: read from guacd TCP, write to agentSend channel.
	go func() {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, err := t.guacdConn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case t.agentSend <- data:
				case <-t.done:
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					slog.Debug("guacd read error", "tunnel_id", t.tunnelID, "error", err)
				}
				return
			}
		}
	}()

	// agent → guacd: read from agentRecv channel, write to guacd TCP.
	go func() {
		defer wg.Done()
		for {
			select {
			case data, ok := <-t.agentRecv:
				if !ok {
					return
				}
				if _, err := t.guacdConn.Write(data); err != nil {
					slog.Debug("guacd write error", "tunnel_id", t.tunnelID, "error", err)
					return
				}
			case <-t.done:
				return
			}
		}
	}()

	wg.Wait()
	tm.closeTunnel(t.tunnelID)
}

// closeTunnel tears down a tunnel and removes it from the map.
func (tm *tunnelManager) closeTunnel(tunnelID string) {
	tm.mu.Lock()
	t, ok := tm.tunnels[tunnelID]
	if ok {
		delete(tm.tunnels, tunnelID)
	}
	tm.mu.Unlock()

	if !ok {
		return
	}

	// Signal done and close connections.
	select {
	case <-t.done:
		// Already closed.
	default:
		close(t.done)
	}
	t.guacdConn.Close()

	slog.Info("scrier tunnel closed",
		"tunnel_id", tunnelID,
		"envoy_id", t.envoyID,
		"hostname", t.hostname,
		"duration", time.Since(t.startedAt).Round(time.Second),
	)
}

// getTunnel returns an active tunnel by ID.
func (tm *tunnelManager) getTunnel(tunnelID string) *activeTunnel {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.tunnels[tunnelID]
}

// activeSessions returns the count of active tunnels.
func (tm *tunnelManager) activeSessions() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.tunnels)
}

// --- Exported methods implementing scrier.TunnelOpener ---

// OpenTunnel creates a tunnel with a local TCP proxy that forwards to the
// agent's localhost:port via the gRPC tunnel. Returns the proxy address
// for guacd to connect to. The done channel closes when the tunnel ends.
func (tm *tunnelManager) OpenTunnel(envoyID, hostname, protocol string, targetPort uint32) (string, string, chan struct{}, error) {
	// Validate port.
	if !tm.allowedPorts[targetPort] {
		return "", "", nil, fmt.Errorf("port %d not in allowed list", targetPort)
	}

	// Check session limit.
	tm.mu.RLock()
	count := len(tm.tunnels)
	tm.mu.RUnlock()
	if count >= tm.maxSessions {
		return "", "", nil, fmt.Errorf("max sessions (%d) reached", tm.maxSessions)
	}

	tunnelID := uuid.New().String()

	// Start a local TCP listener on a random port. This is the proxy that
	// guacd will connect to — it forwards bytes through the gRPC tunnel
	// to the agent's localhost:targetPort.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, fmt.Errorf("start local proxy listener: %w", err)
	}
	proxyAddr := listener.Addr().String()

	t := &activeTunnel{
		tunnelID:   tunnelID,
		envoyID:    envoyID,
		hostname:   hostname,
		protocol:   protocol,
		targetPort: targetPort,
		startedAt:  time.Now(),
		agentRecv:  make(chan []byte, 64),
		agentSend:  make(chan []byte, 64),
		done:       make(chan struct{}),
	}

	tm.mu.Lock()
	tm.tunnels[tunnelID] = t
	tm.mu.Unlock()

	// Assign the tunnel to the waiting agent TunnelStream handler.
	if !tm.assignTunnel(envoyID, t) {
		listener.Close()
		tm.closeTunnel(tunnelID)
		return "", "", nil, fmt.Errorf("no agent tunnel stream available for envoy %s", envoyID)
	}

	slog.Info("scrier tunnel opened with local proxy",
		"tunnel_id", tunnelID,
		"envoy_id", envoyID,
		"hostname", hostname,
		"protocol", protocol,
		"port", targetPort,
		"proxy", proxyAddr,
	)

	// Send TunnelRequest to the agent via its existing AgentStream.
	// The agent will open a new TunnelStream connection in response.
	if tm.streamMgr != nil {
		msg := &pb.ServerStreamMessage{
			Msg: &pb.ServerStreamMessage_TunnelRequest{
				TunnelRequest: &pb.TunnelRequest{
					RequestId: tunnelID,
				},
			},
		}
		if !tm.streamMgr.dispatchPush(envoyID, msg) {
			listener.Close()
			tm.closeTunnel(tunnelID)
			return "", "", nil, fmt.Errorf("agent %s is not connected via stream", envoyID)
		}
	}

	// Wait for the agent to open its TunnelStream and get assigned.
	// The TunnelStream handler calls registerAgentStream → assignTunnel.
	// Give the agent up to 10 seconds to connect.
	agentConnected := make(chan bool, 1)
	go func() {
		deadline := time.After(10 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline:
				agentConnected <- false
				return
			case <-ticker.C:
				// Check if the tunnel has been assigned (agent opened TunnelStream).
				tm.mu.RLock()
				_, assigned := tm.agentStreams[envoyID]
				tm.mu.RUnlock()
				if !assigned {
					// Agent stream was consumed by assignTunnel — tunnel is active.
					agentConnected <- true
					return
				}
			}
		}
	}()

	// Accept one connection from guacd on the proxy listener, then bridge it
	// to the agent channels.
	go func() {
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			slog.Debug("proxy accept error", "tunnel_id", tunnelID, "error", err)
			tm.closeTunnel(tunnelID)
			return
		}
		t.guacdConn = conn
		tm.bridgeGuacdToAgent(t)
	}()

	return tunnelID, proxyAddr, t.done, nil
}

// GuacdAddr returns the guacd TCP address.
func (tm *tunnelManager) GuacdAddr() string {
	return tm.guacdAddr
}

// HasAgentStream checks if an agent has a TunnelStream waiting.
func (tm *tunnelManager) HasAgentStream(envoyID string) bool {
	return tm.hasAgentStream(envoyID)
}

// CloseTunnel tears down a tunnel by ID.
func (tm *tunnelManager) CloseTunnel(tunnelID string) {
	tm.closeTunnel(tunnelID)
}

// ActiveSessions returns the count of active tunnels.
func (tm *tunnelManager) ActiveSessions() int {
	return tm.activeSessions()
}
