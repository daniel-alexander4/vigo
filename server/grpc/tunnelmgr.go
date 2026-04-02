// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/fleet"
	vigoscrier "github.com/daniel-alexander4/vigo/server/scrier"
	"github.com/google/uuid"
)

// tunnelManager tracks active Scrier remote access tunnels.
// Each tunnel bridges guacd TCP ↔ agent gRPC TunnelData.
type tunnelManager struct {
	mu            sync.RWMutex
	tunnels       map[string]*activeTunnel          // tunnel_id → tunnel
	agentStreams  map[string]chan *activeTunnel       // envoy_id → pending tunnel channel
	guacdAddr     string                             // e.g., "guacd:4822"
	proxyHostname string                             // hostname guacd uses to reach this server (e.g., "vigo")
	allowedPorts  map[uint32]bool
	maxSessions   int
	streamMgr     *streamManager                    // sends TunnelRequest via AgentStream
	fleetIndex    *fleet.FleetIndex                 // sets TunnelRequested flag for unary agents
}

// activeTunnel represents a single remote access session.
type activeTunnel struct {
	tunnelID   string
	envoyID    string
	hostname   string
	protocol   string
	targetPort uint32
	startedAt  time.Time

	// SSH pubkey auth fields (set by HandleWebSocket, consumed by runTunnelRelay).
	sshVerifyPubkey    string // web user's SSH pubkey for authorized_keys check
	sshEphemeralPubkey string // per-session pubkey installed temporarily on the agent
	sshTargetUsername  string // Unix username — agent only checks this user's authorized_keys

	// SSH username discovered by the agent from authorized_keys.
	// Set by runTunnelRelay after receiving TunnelOpenResult.
	sshUsername   string
	sshUsernameCh chan string // signaled after TunnelOpenResult with ssh_username

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
// proxyHostname is the hostname that guacd uses to reach this server's
// tunnel proxy listeners (e.g., "vigo" on a Docker Compose network).
func NewTunnelManager(guacdAddr, proxyHostname string, allowedPorts []uint32, maxSessions int, sm *streamManager, fi *fleet.FleetIndex) *tunnelManager {
	ports := make(map[uint32]bool, len(allowedPorts))
	for _, p := range allowedPorts {
		ports[p] = true
	}
	return &tunnelManager{
		tunnels:       make(map[string]*activeTunnel),
		agentStreams:  make(map[string]chan *activeTunnel),
		guacdAddr:     guacdAddr,
		proxyHostname: proxyHostname,
		allowedPorts: ports,
		maxSessions:  maxSessions,
		streamMgr:    sm,
		fleetIndex:   fi,
	}
}

// registerPendingTunnel registers a channel that will receive the activeTunnel
// when the agent's TunnelStream handler is ready. Called by OpenTunnel.
func (tm *tunnelManager) registerPendingTunnel(envoyID string, ch chan *activeTunnel) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.agentStreams[envoyID] = ch
}

// unregisterPendingTunnel removes a pending tunnel registration.
func (tm *tunnelManager) unregisterPendingTunnel(envoyID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.agentStreams, envoyID)
}

// deliverPendingTunnel is called by the TunnelStream handler when the agent
// connects. It finds the pending tunnel for this envoy and returns it.
// Returns nil if no tunnel is pending (shouldn't happen in normal flow).
func (tm *tunnelManager) deliverPendingTunnel(envoyID string) *activeTunnel {
	// Clear the TunnelRequested flag now that the agent connected.
	if tm.fleetIndex != nil {
		tm.fleetIndex.ClearTunnelRequested(envoyID)
	}

	tm.mu.Lock()
	ch, ok := tm.agentStreams[envoyID]
	if !ok {
		tm.mu.Unlock()
		return nil
	}
	delete(tm.agentStreams, envoyID)
	tm.mu.Unlock()

	// Find the tunnel for this envoy.
	tm.mu.RLock()
	var tunnel *activeTunnel
	for _, t := range tm.tunnels {
		if t.envoyID == envoyID {
			tunnel = t
			break
		}
	}
	tm.mu.RUnlock()

	if tunnel == nil {
		return nil
	}

	// Signal the waiting OpenTunnel that the agent is ready.
	select {
	case ch <- tunnel:
	default:
	}

	return tunnel
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
		readCount := 0
		totalBytes := 0
		lastLog := time.Now()
		for {
			n, err := t.guacdConn.Read(buf)
			readCount++
			totalBytes += n
			if readCount <= 3 && n > 0 {
				preview := string(buf[:min(n, 200)])
				slog.Info("scrier proxy: guacd→agent data",
					"tunnel_id", t.tunnelID, "read_num", readCount, "n", n, "preview", preview)
			}
			if time.Since(lastLog) > 5*time.Second {
				slog.Info("scrier proxy: guacd→agent flow",
					"tunnel_id", t.tunnelID, "reads", readCount, "total_bytes", totalBytes)
				lastLog = time.Now()
			}
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
				slog.Info("scrier proxy: guacd→agent ended",
					"tunnel_id", t.tunnelID, "error", err, "reads", readCount, "total_bytes", totalBytes)
				return
			}
		}
	}()

	// agent → guacd: read from agentRecv channel, write to guacd TCP.
	go func() {
		defer wg.Done()
		writeCount := 0
		totalBytes := 0
		lastLog := time.Now()
		for {
			select {
			case data, ok := <-t.agentRecv:
				if !ok {
					return
				}
				writeCount++
				totalBytes += len(data)
				if writeCount <= 3 {
					preview := string(data[:min(len(data), 200)])
					slog.Info("scrier proxy: agent→guacd data",
						"tunnel_id", t.tunnelID, "write_num", writeCount, "n", len(data), "preview", preview)
				}
				if time.Since(lastLog) > 5*time.Second {
					slog.Info("scrier proxy: agent→guacd flow",
						"tunnel_id", t.tunnelID, "writes", writeCount, "total_bytes", totalBytes)
					lastLog = time.Now()
				}
				if _, err := t.guacdConn.Write(data); err != nil {
					slog.Info("scrier proxy: agent→guacd ended",
						"tunnel_id", t.tunnelID, "error", err, "writes", writeCount, "total_bytes", totalBytes)
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
	if t.guacdConn != nil {
		t.guacdConn.Close()
	}

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

// OpenTunnel creates a tunnel to the agent's localhost:port on demand.
//
// Flow:
// 1. Register a pending tunnel for this envoy
// 2. Send TunnelRequest to agent via its existing AgentStream
// 3. Wait for the agent to open a TunnelStream (up to 10s)
// 4. The TunnelStream handler picks up the pending tunnel and starts relaying
// 5. Start a local TCP proxy listener for guacd to connect to
//
// Returns the proxy address for guacd and a done channel.
func (tm *tunnelManager) OpenTunnel(envoyID, hostname, protocol string, targetPort uint32, sshAuth *vigoscrier.SSHPubkeyAuth) (string, string, chan struct{}, error) {
	// Validate port.
	if !tm.allowedPorts[targetPort] {
		return "", "", nil, fmt.Errorf("port %d not in allowed list", targetPort)
	}

	tunnelID := uuid.New().String()

	t := &activeTunnel{
		tunnelID:      tunnelID,
		envoyID:       envoyID,
		hostname:      hostname,
		protocol:      protocol,
		targetPort:    targetPort,
		startedAt:     time.Now(),
		agentRecv:     make(chan []byte, 256),
		agentSend:     make(chan []byte, 256),
		done:          make(chan struct{}),
		sshUsernameCh: make(chan string, 1),
	}
	if sshAuth != nil {
		t.sshVerifyPubkey = sshAuth.VerifyPubkey
		t.sshEphemeralPubkey = sshAuth.EphemeralPubkey
		t.sshTargetUsername = sshAuth.TargetUsername
	}

	// Check session limit and insert atomically to prevent TOCTOU race.
	tm.mu.Lock()
	if len(tm.tunnels) >= tm.maxSessions {
		tm.mu.Unlock()
		return "", "", nil, fmt.Errorf("max sessions (%d) reached", tm.maxSessions)
	}
	tm.tunnels[tunnelID] = t
	tm.mu.Unlock()

	// Step 1: Register a pending tunnel channel. When the agent opens its
	// TunnelStream, the handler will send the tunnel to this channel.
	pendingCh := make(chan *activeTunnel, 1)
	tm.registerPendingTunnel(envoyID, pendingCh)

	// Step 2: Signal the agent to open a TunnelStream.
	// Try the persistent AgentStream first (instant). Fall back to setting
	// the TunnelRequested flag on FleetIndex (delivered on next check-in).
	sentViaStream := false
	if tm.streamMgr != nil {
		msg := &pb.ServerStreamMessage{
			Msg: &pb.ServerStreamMessage_TunnelRequest{
				TunnelRequest: &pb.TunnelRequest{
					RequestId: tunnelID,
				},
			},
		}
		sentViaStream = tm.streamMgr.dispatchPush(envoyID, msg)
	}
	if !sentViaStream {
		// Agent is using unary check-in. Set the flag so the next
		// CheckInResponse tells it to open a TunnelStream.
		if tm.fleetIndex != nil {
			tm.fleetIndex.SetTunnelRequested(envoyID, true)
			tm.fleetIndex.SetFastPoll(envoyID)
			tm.fleetIndex.SignalWake(envoyID)
			slog.Info("scrier tunnel: agent not on stream, signaling wake",
				"tunnel_id", tunnelID,
				"envoy_id", envoyID,
			)
		} else {
			tm.unregisterPendingTunnel(envoyID)
			tm.closeTunnel(tunnelID)
			return "", "", nil, fmt.Errorf("agent %s unreachable (no stream, no fleet index)", envoyID)
		}
	}

	slog.Info("scrier tunnel request sent, waiting for agent",
		"tunnel_id", tunnelID,
		"envoy_id", envoyID,
		"hostname", hostname,
		"protocol", protocol,
		"port", targetPort,
	)

	// Step 3: Wait for the agent to open its TunnelStream (up to 10s).
	// The TunnelStream handler in tunnel.go calls registerAgentStream,
	// which triggers deliverPendingTunnel, which sends to pendingCh.
	select {
	case <-pendingCh:
		// Agent connected and tunnel was assigned.
	case <-time.After(60 * time.Second):
		tm.unregisterPendingTunnel(envoyID)
		tm.closeTunnel(tunnelID)
		return "", "", nil, fmt.Errorf("agent %s did not open TunnelStream within 60s", envoyID)
	}

	// Step 4: Start a local TCP proxy listener for guacd to connect to.
	// Bind all interfaces so guacd (potentially in a separate container) can reach us.
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		tm.closeTunnel(tunnelID)
		return "", "", nil, fmt.Errorf("start local proxy listener: %w", err)
	}
	// Return address with the configured hostname (e.g., "vigo:33143") so
	// guacd in a separate container can resolve and connect to us.
	_, proxyPort, _ := net.SplitHostPort(listener.Addr().String())
	proxyAddr := net.JoinHostPort(tm.proxyHostname, proxyPort)

	slog.Info("scrier tunnel ready",
		"tunnel_id", tunnelID,
		"envoy_id", envoyID,
		"hostname", hostname,
		"protocol", protocol,
		"port", targetPort,
		"proxy", proxyAddr,
	)

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
		// Disable Nagle on both sides of the proxy — SSH packets are small
		// and latency-sensitive. Without this, NEWKEYS and auth packets can
		// stall in the kernel buffer waiting for coalescing.
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		slog.Info("scrier proxy: guacd connected to tunnel proxy",
			"tunnel_id", tunnelID,
			"remote_addr", conn.RemoteAddr().String(),
		)
		tm.bridgeGuacdToAgent(t)
	}()

	return tunnelID, proxyAddr, t.done, nil
}

// GuacdAddr returns the guacd TCP address.
func (tm *tunnelManager) GuacdAddr() string {
	return tm.guacdAddr
}

// HasAgentStream checks if the agent is connected via AgentStream (can receive TunnelRequest).
func (tm *tunnelManager) HasAgentStream(envoyID string) bool {
	if tm.streamMgr == nil {
		return false
	}
	tm.streamMgr.mu.RLock()
	_, ok := tm.streamMgr.streams[envoyID]
	tm.streamMgr.mu.RUnlock()
	return ok
}

// CloseTunnel tears down a tunnel by ID.
func (tm *tunnelManager) CloseTunnel(tunnelID string) {
	tm.closeTunnel(tunnelID)
}

// ActiveSessions returns the count of active tunnels.
func (tm *tunnelManager) ActiveSessions() int {
	return tm.activeSessions()
}

// SSHUsername returns the Unix username discovered by the agent for a tunnel.
// Blocks until the agent responds with TunnelOpenResult (up to 15s).
func (tm *tunnelManager) SSHUsername(tunnelID string) string {
	t := tm.getTunnel(tunnelID)
	if t == nil {
		return ""
	}
	select {
	case username := <-t.sshUsernameCh:
		return username
	case <-time.After(15 * time.Second):
		slog.Warn("scrier: timed out waiting for SSH username from agent",
			"tunnel_id", tunnelID)
		return ""
	case <-t.done:
		return ""
	}
}
