// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package scrier implements the Vigo Scrier remote access feature.
// It bridges browser WebSocket connections through guacd to agent
// gRPC tunnel streams for browser-based RDP/SSH access to envoys.
package scrier

import (
	"bufio"
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"

	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// SSHPubkeyAuth holds the SSH pubkey auth parameters for a Scrier session.
type SSHPubkeyAuth struct {
	VerifyPubkey    string // web user's SSH pubkey for authorized_keys check
	EphemeralPubkey string // per-session pubkey installed temporarily
	EphemeralKey    []byte // PEM-encoded ephemeral private key (for guacd)
}

// TunnelOpener is the interface the WebSocket handler uses to create tunnels.
// Implemented by grpc.tunnelManager (unexported type, accessed via interface).
type TunnelOpener interface {
	// OpenTunnel creates a tunnel to the agent's localhost:port, starts a local
	// TCP proxy on a random port, and returns the proxy address for guacd to
	// connect to. The done channel is closed when the tunnel is torn down.
	OpenTunnel(envoyID, hostname, protocol string, targetPort uint32, sshAuth *SSHPubkeyAuth) (tunnelID string, proxyAddr string, done chan struct{}, err error)
	HasAgentStream(envoyID string) bool
	CloseTunnel(tunnelID string)
	ActiveSessions() int
	GuacdAddr() string
	// SSHUsername returns the Unix username discovered by the agent for a tunnel.
	// Available after the agent responds to TunnelOpen with TunnelOpenResult.
	SSHUsername(tunnelID string) string
}

// AuditRecorder writes tamper-evident audit entries.
type AuditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// UserPubkeyLookup retrieves a user's SSH public key by username.
type UserPubkeyLookup interface {
	GetUserSSHPubkey(ctx context.Context, username string) (string, error)
}

// Handler serves the Scrier WebSocket and session page endpoints.
type Handler struct {
	tunnelOpener  TunnelOpener
	fleetIndex    *fleet.FleetIndex
	queries       *db.Queries
	pubkeyLookup  UserPubkeyLookup
	auditWriter   AuditRecorder
	version       string
	tmplSession   *template.Template
}

// NewHandler creates a Scrier handler.
func NewHandler(opener TunnelOpener, fleetIndex *fleet.FleetIndex, queries *db.Queries, pubkeys UserPubkeyLookup, version string) *Handler {
	tmpl := template.Must(template.New("scrier-session").Parse(scrierSessionHTML))
	return &Handler{
		tunnelOpener:  opener,
		fleetIndex:    fleetIndex,
		queries:       queries,
		pubkeyLookup:  pubkeys,
		version:       version,
		tmplSession:   tmpl,
	}
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *Handler) SetAuditWriter(w AuditRecorder) {
	h.auditWriter = w
}

// RegisterRoutes registers the Scrier endpoints.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /scrier/{id}", h.HandleSession)
	mux.HandleFunc("GET /ws/tunnel/{id}", h.HandleWebSocket)
}

// HandleSession serves the full-page Scrier session template.
func (h *Handler) HandleSession(w http.ResponseWriter, r *http.Request) {
	user := vigoauth.UserFromContext(r.Context())
	if user == nil || !user.IsAdmin() {
		http.Error(w, "admin access required", http.StatusForbidden)
		return
	}

	envoyID := r.PathValue("id")
	envoy, exists, revoked := h.fleetIndex.GetEnvoyLite(envoyID)
	if !exists {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	if revoked {
		http.Error(w, "envoy is revoked", http.StatusForbidden)
		return
	}

	// Determine protocol from query params or auto-detect from traits.
	protocol := r.URL.Query().Get("protocol")
	portStr := r.URL.Query().Get("port")
	if protocol == "" {
		protocol, _ = autoSelectProtocol(envoy.Traits)
	}
	port := uint32(22)
	if portStr != "" {
		if p, err := strconv.ParseUint(portStr, 10, 32); err == nil {
			port = uint32(p)
		}
	} else if protocol == "rdp" {
		port = 3389
	}

	data := struct {
		EnvoyID  string
		Hostname string
		Protocol string
		Port     uint32
		Version  string
	}{
		EnvoyID:  envoyID,
		Hostname: envoy.Hostname,
		Protocol: protocol,
		Port:     port,
		Version:  h.version,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.tmplSession.Execute(w, data)
}

// HandleWebSocket upgrades an HTTP connection to WebSocket and bridges it
// to a guacd tunnel via the agent's TunnelStream.
func (h *Handler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	slog.Info("scrier websocket request received",
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
		"origin", r.Header.Get("Origin"),
		"host", r.Host,
	)

	user := vigoauth.UserFromContext(r.Context())
	if user == nil || !user.IsAdmin() {
		slog.Warn("scrier websocket auth rejected", "user_nil", user == nil)
		http.Error(w, "admin access required", http.StatusForbidden)
		return
	}

	envoyID := r.PathValue("id")
	envoy, exists, revoked := h.fleetIndex.GetEnvoyLite(envoyID)
	if !exists {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	if revoked {
		http.Error(w, "envoy is revoked", http.StatusForbidden)
		return
	}

	// Parse and validate protocol and port.
	protocol := r.URL.Query().Get("protocol")
	portStr := r.URL.Query().Get("port")
	if protocol == "" {
		protocol, _ = autoSelectProtocol(envoy.Traits)
	}
	if protocol != "ssh" && protocol != "rdp" {
		http.Error(w, "invalid protocol: must be ssh or rdp", http.StatusBadRequest)
		return
	}
	port := uint32(22)
	if portStr != "" {
		p, err := strconv.ParseUint(portStr, 10, 32)
		if err != nil || p == 0 || p > 65535 {
			http.Error(w, "invalid port number", http.StatusBadRequest)
			return
		}
		port = uint32(p)
	} else if protocol == "rdp" {
		port = 3389
	}

	// For SSH sessions, look up the web user's SSH pubkey and generate an
	// ephemeral keypair. The agent verifies the user's pubkey against
	// authorized_keys, installs the ephemeral pubkey temporarily, and returns
	// the matching Unix username. guacd authenticates with the ephemeral key.
	var sshAuth *SSHPubkeyAuth
	if protocol == "ssh" && h.pubkeyLookup != nil {
		userPubkey, err := h.pubkeyLookup.GetUserSSHPubkey(r.Context(), user.Username)
		if err != nil {
			slog.Warn("scrier pubkey lookup failed", "user", user.Username, "error", err)
		}
		if userPubkey != "" {
			ephPriv, ephPub, err := GenerateEphemeralSSHKey()
			if err != nil {
				http.Error(w, fmt.Sprintf("failed to generate session key: %v", err), http.StatusInternalServerError)
				return
			}
			sshAuth = &SSHPubkeyAuth{
				VerifyPubkey:    userPubkey,
				EphemeralPubkey: ephPub,
				EphemeralKey:    ephPriv,
			}
		}
	}

	// Open tunnel: sends TunnelRequest to agent via AgentStream, waits for
	// the agent to open a TunnelStream, creates a local TCP proxy that
	// localhost:port via the gRPC tunnel. Returns the proxy address for guacd.
	tunnelID, proxyAddr, done, err := h.tunnelOpener.OpenTunnel(envoyID, envoy.Hostname, protocol, port, sshAuth)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to open tunnel: %v", err), http.StatusInternalServerError)
		return
	}

	// Connect to guacd and perform the Guacamole protocol handshake server-side.
	guacdConn, err := net.DialTimeout("tcp", h.tunnelOpener.GuacdAddr(), 5*time.Second)
	if err != nil {
		h.tunnelOpener.CloseTunnel(tunnelID)
		http.Error(w, fmt.Sprintf("cannot reach guacd: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer guacdConn.Close()

	proxyHost, proxyPort, _ := net.SplitHostPort(proxyAddr)

	// If the agent returned an SSH username via pubkey auth, use it (along
	// with the ephemeral private key) for the guacd handshake.
	var sshUsername string
	var sshPrivateKeyPEM []byte
	if sshAuth != nil {
		sshUsername = h.tunnelOpener.SSHUsername(tunnelID)
		sshPrivateKeyPEM = sshAuth.EphemeralKey
	}

	guacdRW := bufio.NewReadWriter(
		bufio.NewReaderSize(guacdConn, 64*1024),
		bufio.NewWriter(guacdConn),
	)
	connID, err := doGuacdHandshake(guacdRW, protocol, proxyHost, proxyPort, sshUsername, sshPrivateKeyPEM)
	if err != nil {
		slog.Error("scrier guacd handshake failed", "tunnel_id", tunnelID, "error", err)
		h.tunnelOpener.CloseTunnel(tunnelID)
		http.Error(w, fmt.Sprintf("guacd handshake failed: %v", err), http.StatusBadGateway)
		return
	}

	slog.Info("scrier guacd handshake complete",
		"tunnel_id", tunnelID,
		"envoy_id", envoyID,
		"hostname", envoy.Hostname,
		"protocol", protocol,
		"port", port,
		"proxy", proxyAddr,
		"connection_id", connID,
	)

	// Record session in DB.
	if h.queries != nil {
		_, _ = h.queries.InsertScrierSession(r.Context(), db.InsertScrierSessionParams{
			EnvoyID:    envoyID,
			UserID:     user.ID,
			Username:   user.Username,
			Protocol:   protocol,
			TargetPort: int64(port),
			TunnelID:   tunnelID,
		})
	}

	// Audit event.
	if h.auditWriter != nil {
		_ = h.auditWriter.Record(r.Context(), "scrier.connect", user.Username, "web_ui",
			fmt.Sprintf("opened %s session to %s (port %d)", protocol, envoy.Hostname, port), "")
	}

	startedAt := time.Now()
	slog.Info("scrier websocket session started",
		"tunnel_id", tunnelID,
		"envoy_id", envoyID,
		"hostname", envoy.Hostname,
		"protocol", protocol,
		"port", port,
		"proxy", proxyAddr,
		"user", user.Username,
	)

	// Upgrade to WebSocket.
	// Origin check: admin session cookie is the primary CSRF boundary.
	// InsecureSkipVerify allows connections from any origin (e.g., IP
	// access, reverse proxies). Scrier endpoints require admin auth.
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		h.tunnelOpener.CloseTunnel(tunnelID)
		return
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "session ended")

	ctx := r.Context()
	wsDone := make(chan struct{})

	// Mutex protects WebSocket writes — nhooyr.io/websocket does not support
	// concurrent writers. Both goroutines below write to wsConn.
	var wsMu sync.Mutex
	writeWS := func(data []byte) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return wsConn.Write(ctx, websocket.MessageText, data)
	}

	// guacd → WebSocket: raw relay of display instructions.
	// Read from the buffered reader (may have data from handshake).
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := guacdRW.Read(buf)
			if n > 0 {
				if writeErr := writeWS(buf[:n]); writeErr != nil {
					slog.Info("scrier ws write error", "tunnel_id", tunnelID, "error", writeErr)
					return
				}
			}
			if err != nil {
				slog.Info("scrier guacd read done", "tunnel_id", tunnelID, "error", err)
				return
			}
		}
	}()

	// WebSocket → guacd: filter tunnel-level pings, forward everything else.
	go func() {
		defer close(wsDone)
		for {
			_, data, err := wsConn.Read(ctx)
			if err != nil {
				slog.Info("scrier ws read done", "tunnel_id", tunnelID, "error", err)
				return
			}

			// Handle Guacamole tunnel-level pings (empty opcode keepalive).
			// These are internal to the WebSocketTunnel and must not reach guacd.
			if pong, isPing := tunnelPong(string(data)); isPing {
				_ = writeWS([]byte(pong))
				continue
			}

			// Forward client instructions (mouse, key, size, clipboard, etc.) to guacd.
			if _, err := guacdConn.Write(data); err != nil {
				slog.Info("scrier guacd write error", "tunnel_id", tunnelID, "error", err)
				return
			}
		}
	}()

	// Wait for either side to close.
	select {
	case <-wsDone:
	case <-done:
	case <-ctx.Done():
	}

	h.tunnelOpener.CloseTunnel(tunnelID)

	duration := time.Since(startedAt).Round(time.Second)

	// Record session end in DB.
	if h.queries != nil {
		_ = h.queries.EndScrierSession(context.Background(), db.EndScrierSessionParams{
			Status:   "closed",
			TunnelID: tunnelID,
		})
	}

	// Audit event.
	if h.auditWriter != nil {
		_ = h.auditWriter.Record(context.Background(), "scrier.disconnect", user.Username, "web_ui",
			fmt.Sprintf("closed %s session to %s after %s", protocol, envoy.Hostname, duration), "")
	}

	slog.Info("scrier websocket session ended",
		"tunnel_id", tunnelID,
		"hostname", envoy.Hostname,
		"user", user.Username,
		"duration", duration,
	)
}


// autoSelectProtocol picks the best protocol based on envoy traits.
func autoSelectProtocol(traits map[string]string) (protocol string, port uint32) {
	family := traits["os.family"]
	switch {
	case strings.EqualFold(family, "windows"):
		return "rdp", 3389
	default:
		return "ssh", 22
	}
}
