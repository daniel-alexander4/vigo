// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package scrier implements the Vigo Scrier remote access feature.
// It bridges browser WebSocket connections through guacd to agent
// gRPC tunnel streams for browser-based RDP/SSH access to envoys.
package scrier

import (
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

	// Check if the agent has a TunnelStream open.
	if !h.tunnelOpener.HasAgentStream(envoyID) {
		http.Error(w, "envoy does not have a tunnel stream open — agent may need to reconnect", http.StatusServiceUnavailable)
		return
	}

	// Open tunnel: creates a local TCP proxy that forwards to the agent's
	// localhost:port via the gRPC tunnel. Returns the proxy address for guacd.
	tunnelID, proxyAddr, done, err := h.tunnelOpener.OpenTunnel(envoyID, envoy.Hostname, protocol, port)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to open tunnel: %v", err), http.StatusInternalServerError)
		return
	}

	// Connect to guacd and perform the Guacamole protocol handshake.
	// guacd will connect to the proxy address (which tunnels to the agent).
	guacdConn, err := net.DialTimeout("tcp", h.tunnelOpener.GuacdAddr(), 5*time.Second)
	if err != nil {
		h.tunnelOpener.CloseTunnel(tunnelID)
		http.Error(w, fmt.Sprintf("cannot reach guacd: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer guacdConn.Close()

	// Send Guacamole handshake to guacd: select protocol, then connect with params.
	// The proxy address host:port tells guacd where to connect for the actual SSH/RDP.
	proxyHost, proxyPort, _ := net.SplitHostPort(proxyAddr)
	guacHandshake := fmt.Sprintf("6.select,1.c,%d.%s;", len(protocol), protocol)
	switch protocol {
	case "ssh":
		guacHandshake += fmt.Sprintf(
			"7.connect,%d.%s,%d.%s,%d.%s,%d.%s;",
			len(proxyHost), proxyHost,
			len(proxyPort), proxyPort,
			1, "0",  // server-alive-interval placeholder
			0, "",   // no password — user authenticates in the SSH session
		)
	case "rdp":
		guacHandshake += fmt.Sprintf(
			"7.connect,%d.%s,%d.%s,%d.%s;",
			len(proxyHost), proxyHost,
			len(proxyPort), proxyPort,
			1, "0",
		)
	}

	// Actually, the Guacamole protocol handshake is more complex.
	// The simplest correct approach: let the Guacamole JS client handle the
	// handshake by bridging the WebSocket directly to guacd TCP. The JS client
	// sends the correct "select" + "size" + "audio" + "video" + "connect"
	// instructions, and guacd responds with "args" then renders the session.
	//
	// We just need to tell guacd the connection parameters by injecting them
	// when the client sends "connect". But this requires protocol-aware parsing.
	//
	// Simpler: use guacd's "connection ID" feature, OR just bridge raw bytes
	// and configure guacd connection params via environment or a config file.
	//
	// Simplest correct approach: bridge WebSocket ↔ guacd TCP directly.
	// The Guacamole JS client initiates the handshake, we just relay.
	// But we need guacd to know where to connect (our proxy address).
	// guacd gets this from the "connect" instruction sent by the client.
	//
	// Solution: we intercept the "connect" instruction from the client,
	// inject the hostname and port, then forward to guacd.

	// Reset — we'll do the simpler raw bridge approach with connection param injection.
	_ = guacHandshake // unused — the JS client drives the handshake

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

	// Bridge: WebSocket ↔ guacd TCP.
	// The Guacamole JS client drives the protocol handshake with guacd.
	// We intercept the "connect" instruction to inject the proxy host/port
	// so guacd knows where to reach the agent's SSH/RDP service.
	wsDone := make(chan struct{})

	// WebSocket → guacd: relay client instructions to guacd.
	go func() {
		defer close(wsDone)
		connectSent := false
		for {
			_, data, err := wsConn.Read(ctx)
			if err != nil {
				return
			}
			// Intercept the "connect" instruction to inject connection params.
			// The Guacamole client sends: "7.connect,<params>;".
			// We rewrite it to include hostname and port of our local proxy.
			if !connectSent {
				s := string(data)
				if strings.Contains(s, "connect") {
					data = []byte(buildConnectInstruction(protocol, proxyHost, proxyPort))
					connectSent = true
				}
			}
			if _, err := guacdConn.Write(data); err != nil {
				return
			}
		}
	}()

	// guacd → WebSocket: relay guacd responses to the browser.
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := guacdConn.Read(buf)
			if n > 0 {
				if writeErr := wsConn.Write(ctx, websocket.MessageText, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
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

// buildConnectInstruction creates a Guacamole "connect" instruction with the
// correct connection parameters for the given protocol and proxy address.
func buildConnectInstruction(protocol, host, port string) string {
	// Guacamole protocol uses length-prefixed values: "5.value".
	lp := func(s string) string { return fmt.Sprintf("%d.%s", len(s), s) }

	switch protocol {
	case "ssh":
		return fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s;",
			lp("connect"),
			lp(host),     // hostname
			lp(port),     // port
			lp(""),       // username (user types it)
			lp(""),       // password (user types it)
			lp(""),       // private-key
			lp(""),       // passphrase
			lp("xterm-256color"), // terminal-type
			lp("120"),    // width (cols)
			lp("40"),     // height (rows)
			lp("0"),      // dpi
			lp(""),       // color-scheme
			lp("true"),   // enable-sftp
		)
	case "rdp":
		return fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s,%s,%s;",
			lp("connect"),
			lp(host),     // hostname
			lp(port),     // port
			lp(""),       // username
			lp(""),       // password
			lp(""),       // domain
			lp("true"),   // ignore-cert
			lp("1024"),   // width
			lp("768"),    // height
			lp("96"),     // dpi
		)
	default:
		return fmt.Sprintf("%s,%s,%s;", lp("connect"), lp(host), lp(port))
	}
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
