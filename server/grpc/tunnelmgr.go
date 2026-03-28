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
func NewTunnelManager(guacdAddr string, allowedPorts []uint32, maxSessions int) *tunnelManager {
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

// openTunnel creates a new tunnel for the given envoy. It connects to guacd
// and starts the guacd↔agent byte bridge. Returns the tunnel for the
// TunnelStream handler to attach to.
func (tm *tunnelManager) openTunnel(envoyID, hostname, protocol string, targetPort uint32) (*activeTunnel, error) {
	// Validate port.
	if !tm.allowedPorts[targetPort] {
		return nil, fmt.Errorf("port %d not in allowed list", targetPort)
	}

	// Check session limit.
	tm.mu.RLock()
	count := len(tm.tunnels)
	tm.mu.RUnlock()
	if count >= tm.maxSessions {
		return nil, fmt.Errorf("max sessions (%d) reached", tm.maxSessions)
	}

	// Connect to guacd.
	guacdConn, err := net.DialTimeout("tcp", tm.guacdAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to guacd at %s: %w", tm.guacdAddr, err)
	}

	tunnelID := uuid.New().String()
	t := &activeTunnel{
		tunnelID:   tunnelID,
		envoyID:    envoyID,
		hostname:   hostname,
		protocol:   protocol,
		targetPort: targetPort,
		startedAt:  time.Now(),
		guacdConn:  guacdConn,
		agentRecv:  make(chan []byte, 64),
		agentSend:  make(chan []byte, 64),
		done:       make(chan struct{}),
	}

	tm.mu.Lock()
	tm.tunnels[tunnelID] = t
	tm.mu.Unlock()

	slog.Info("scrier tunnel opened",
		"tunnel_id", tunnelID,
		"envoy_id", envoyID,
		"hostname", hostname,
		"protocol", protocol,
		"port", targetPort,
	)

	// Start the guacd↔agent bridge.
	go tm.bridgeGuacdToAgent(t)

	return t, nil
}

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
