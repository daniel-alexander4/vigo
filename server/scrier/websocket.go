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
	"time"

	"nhooyr.io/websocket"

	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// TunnelOpener is the interface the WebSocket handler uses to create tunnels.
// Implemented by grpc.tunnelManager (unexported type, accessed via interface).
type TunnelOpener interface {
	// OpenTunnel creates a tunnel to the agent's localhost:port, starts a local
	// TCP proxy on a random port, and returns the proxy address for guacd to
	// connect to. The done channel is closed when the tunnel is torn down.
	OpenTunnel(envoyID, hostname, protocol string, targetPort uint32) (tunnelID string, proxyAddr string, done chan struct{}, err error)
	HasAgentStream(envoyID string) bool
	CloseTunnel(tunnelID string)
	ActiveSessions() int
	GuacdAddr() string
}

// AuditRecorder writes tamper-evident audit entries.
type AuditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// Handler serves the Scrier WebSocket and session page endpoints.
type Handler struct {
	tunnelOpener TunnelOpener
	fleetIndex   *fleet.FleetIndex
	queries      *db.Queries
	auditWriter  AuditRecorder
	version      string
	tmplSession  *template.Template
}

// NewHandler creates a Scrier handler.
func NewHandler(opener TunnelOpener, fleetIndex *fleet.FleetIndex, queries *db.Queries, version string) *Handler {
	tmpl := template.Must(template.New("scrier-session").Parse(scrierSessionHTML))
	return &Handler{
		tunnelOpener: opener,
		fleetIndex:   fleetIndex,
		queries:      queries,
		version:      version,
		tmplSession:  tmpl,
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

	// Parse protocol and port.
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

	// Open tunnel: sends TunnelRequest to agent via AgentStream, waits for
	// the agent to open a TunnelStream, creates a local TCP proxy that
	// localhost:port via the gRPC tunnel. Returns the proxy address for guacd.
	tunnelID, proxyAddr, done, err := h.tunnelOpener.OpenTunnel(envoyID, envoy.Hostname, protocol, port)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to open tunnel: %v", err), http.StatusInternalServerError)
		return
	}

	// Connect to guacd TCP. The Guacamole JS client drives the protocol
	// handshake (select → args → size/audio/video/image → connect → ready).
	// We relay instructions between the WebSocket and guacd, but rewrite
	// the "connect" instruction to inject our local proxy address as the
	// target hostname:port (the proxy tunnels to the agent's localhost).
	guacdConn, err := net.DialTimeout("tcp", h.tunnelOpener.GuacdAddr(), 5*time.Second)
	if err != nil {
		h.tunnelOpener.CloseTunnel(tunnelID)
		http.Error(w, fmt.Sprintf("cannot reach guacd: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer guacdConn.Close()

	proxyHost, proxyPort, _ := net.SplitHostPort(proxyAddr)

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

	// Bridge: WebSocket ↔ guacd TCP with instruction-aware rewriting.
	// The Guacamole JS client drives the handshake with guacd. We relay
	// all instructions, but rewrite the "connect" instruction to inject
	// the proxy address as the target hostname:port.
	wsDone := make(chan struct{})

	// Track the arg names guacd sends so we can build the correct connect values.
	var guacdArgNames []string

	// WebSocket → guacd: relay client instructions, rewrite "connect".
	go func() {
		defer close(wsDone)
		for {
			_, data, err := wsConn.Read(ctx)
			if err != nil {
				return
			}
			// Parse the instruction to check if it's "connect".
			inst, parseErr := parseInstruction(string(data))
			if parseErr == nil && inst.Opcode == "connect" && len(guacdArgNames) > 0 {
				// Rewrite connect args with our proxy address.
				rewritten := &guacInstruction{
					Opcode: "connect",
					Args:   buildConnectArgs(guacdArgNames, protocol, proxyHost, proxyPort),
				}
				data = []byte(rewritten.String())
				slog.Debug("scrier rewrote connect instruction",
					"tunnel_id", tunnelID,
					"proxy", proxyAddr,
					"args", guacdArgNames,
				)
			}
			if _, err := guacdConn.Write(data); err != nil {
				return
			}
		}
	}()

	// guacd → WebSocket: relay guacd responses, capture "args" for rewriting.
	go func() {
		guacdReader := bufio.NewReaderSize(guacdConn, 64*1024)
		for {
			inst, raw, err := readInstruction(guacdReader)
			if err != nil {
				return
			}
			// Capture the arg names so we can rewrite "connect" correctly.
			if inst != nil && inst.Opcode == "args" {
				guacdArgNames = inst.Args
			}
			if writeErr := wsConn.Write(ctx, websocket.MessageText, raw); writeErr != nil {
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
