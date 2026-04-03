// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package scrier implements the Vigo Scrier remote access feature.
// SSH sessions use xterm.js with a Go SSH client (no guacd).
// RDP sessions use Guacamole (guacd) for protocol translation.
//
// Two-phase architecture:
//
//	Phase 1 (HTTP): POST /api/v1/scrier/connect → background setup (tunnel + SSH/guacd)
//	Phase 2 (WebSocket): GET /ws/scrier/{session_id} → relay
package scrier

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"nhooyr.io/websocket"

	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/google/uuid"
)

// SSHPubkeyAuth holds the SSH pubkey auth parameters for a Scrier session.
type SSHPubkeyAuth struct {
	VerifyPubkey    string // web user's SSH pubkey for authorized_keys check
	EphemeralPubkey string // per-session pubkey installed temporarily
	EphemeralKey    []byte // PEM-encoded ephemeral private key
	TargetUsername  string // Unix username — agent only checks this user's authorized_keys
}

// TunnelOpener is the interface the WebSocket handler uses to create tunnels.
type TunnelOpener interface {
	OpenTunnel(envoyID, hostname, protocol string, targetPort uint32, sshAuth *SSHPubkeyAuth) (tunnelID string, proxyAddr string, done chan struct{}, err error)
	HasAgentStream(envoyID string) bool
	CloseTunnel(tunnelID string)
	ActiveSessions() int
	GuacdAddr() string
	SSHUsername(tunnelID string) string
}

// AuditRecorder writes tamper-evident audit entries.
type AuditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// ConfigKeyLookup finds authorized_keys for a Unix username from config modules.
type ConfigKeyLookup interface {
	UserAuthorizedKeys(username string) string
}

// --- Session store ---

type sessionStatus string

const (
	statusWaiting    sessionStatus = "waiting"
	statusConnecting sessionStatus = "connecting"
	statusReady      sessionStatus = "ready"
	statusError      sessionStatus = "error"
)

// scrierSession holds state for a single Scrier connection setup.
type scrierSession struct {
	mu        sync.Mutex
	id        string
	envoyID   string
	hostname  string
	protocol  string
	port      uint32
	user      string // web UI username
	status    sessionStatus
	errorMsg  string
	createdAt time.Time

	// Set during setup:
	tunnelID   string
	tunnelDone chan struct{}
	sshUsername string

	// SSH-specific (xterm.js path):
	sshClient  *ssh.Client
	sshSession *ssh.Session
	ptyIn      io.WriteCloser
	ptyOut     io.Reader

	// Display dimensions from browser viewport.
	width  int
	height int

	// RDP-specific (guacd path):
	guacdConn     net.Conn
	guacdRW       *bufio.ReadWriter
	guacdConnID   string
	sshPrivateKey []byte
}

func (s *scrierSession) setStatus(st sessionStatus, errMsg string) {
	s.mu.Lock()
	prev := s.status
	s.status = st
	s.errorMsg = errMsg
	s.mu.Unlock()
	slog.Info("scrier session state",
		"session_id", s.id,
		"from", string(prev),
		"to", string(st),
		"error", errMsg,
	)
}

func (s *scrierSession) getStatus() (sessionStatus, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.errorMsg
}

// --- Handler ---

// Handler serves Scrier endpoints: session page, connect API, status API, WebSocket relay.
type Handler struct {
	tunnelOpener TunnelOpener
	fleetIndex   *fleet.FleetIndex
	queries      *db.Queries
	configKeys   ConfigKeyLookup
	auditWriter  AuditRecorder
	version      string
	connectWait  string // human-readable max wait (check-in interval + 20%)
	tmplSSH      *template.Template
	tmplRDP      *template.Template

	sessionsMu sync.Mutex
	sessions   map[string]*scrierSession
}

// NewHandler creates a Scrier handler.
func NewHandler(opener TunnelOpener, fleetIndex *fleet.FleetIndex, queries *db.Queries, configKeys ConfigKeyLookup, version string, checkinInterval time.Duration) *Handler {
	tmplSSH := template.Must(template.New("scrier-ssh").Parse(scrierSessionSSHHTML))
	tmplRDP := template.Must(template.New("scrier-rdp").Parse(scrierSessionRDPHTML))

	// Compute max wait: check-in interval + 20% jitter.
	maxWait := checkinInterval + checkinInterval/5
	waitStr := formatDuration(maxWait)

	return &Handler{
		tunnelOpener: opener,
		fleetIndex:   fleetIndex,
		queries:      queries,
		configKeys:   configKeys,
		version:      version,
		connectWait:  waitStr,
		tmplSSH:      tmplSSH,
		tmplRDP:      tmplRDP,
		sessions:     make(map[string]*scrierSession),
	}
}

// formatDuration returns a human-friendly duration string (e.g. "6 minutes").
func formatDuration(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%d seconds", secs)
	}
	mins := (secs + 59) / 60 // round up
	if mins == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", mins)
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *Handler) SetAuditWriter(w AuditRecorder) {
	h.auditWriter = w
}

// RegisterRoutes registers the Scrier endpoints.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /scrier/{id}", h.HandleSession)
	mux.HandleFunc("POST /api/v1/scrier/connect", h.HandleConnect)
	mux.HandleFunc("GET /api/v1/scrier/sessions/{id}/status", h.HandleSessionStatus)
	mux.HandleFunc("GET /ws/scrier/{id}", h.HandleRelay)
}

// --- Phase 0: Session page ---

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
	} else {
		switch protocol {
		case "rdp":
			port = 3389
		case "vnc":
			port = 5900
		}
	}

	data := struct {
		EnvoyID     string
		Hostname    string
		Protocol    string
		Port        uint32
		Version     string
		ConnectWait string
	}{
		EnvoyID:     envoyID,
		Hostname:    envoy.Hostname,
		Protocol:    protocol,
		Port:        port,
		Version:     h.version,
		ConnectWait: h.connectWait,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	switch protocol {
	case "ssh":
		h.tmplSSH.Execute(w, data)
	default:
		h.tmplRDP.Execute(w, data) // RDP and VNC both use the Guacamole template
	}
}

// --- Phase 1: Connect (setup) ---

type connectRequest struct {
	EnvoyID  string `json:"envoy_id"`
	Protocol string `json:"protocol"`
	Port     uint32 `json:"port"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// HandleConnect starts a Scrier session setup in the background.
func (h *Handler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	user := vigoauth.UserFromContext(r.Context())
	if user == nil || !user.IsAdmin() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Protocol != "ssh" && req.Protocol != "rdp" && req.Protocol != "vnc" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "protocol must be ssh, rdp, or vnc"})
		return
	}
	if req.Port == 0 || req.Port > 65535 {
		switch req.Protocol {
		case "rdp":
			req.Port = 3389
		case "vnc":
			req.Port = 5900
		default:
			req.Port = 22
		}
	}

	envoy, exists, revoked := h.fleetIndex.GetEnvoyLite(req.EnvoyID)
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "envoy not found"})
		return
	}
	if revoked {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "envoy is revoked"})
		return
	}

	// Default to 1024x768 if browser didn't send dimensions.
	width, height := req.Width, req.Height
	if width <= 0 || height <= 0 {
		width, height = 1024, 768
	}

	sess := &scrierSession{
		id:        uuid.New().String(),
		envoyID:   req.EnvoyID,
		hostname:  envoy.Hostname,
		protocol:  req.Protocol,
		port:      req.Port,
		width:     width,
		height:    height,
		user:      user.Username,
		status:    statusWaiting,
		createdAt: time.Now(),
	}

	h.sessionsMu.Lock()
	h.sessions[sess.id] = sess
	h.sessionsMu.Unlock()

	go h.setupSession(sess, user)

	slog.Info("scrier session created",
		"session_id", sess.id,
		"envoy_id", req.EnvoyID,
		"hostname", envoy.Hostname,
		"protocol", req.Protocol,
		"port", req.Port,
		"user", user.Username,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"session_id": sess.id,
		"status":     string(statusWaiting),
	})
}

// setupSession runs in a background goroutine. It opens the tunnel to the
// agent, then either connects via Go SSH client (for SSH) or guacd (for RDP).
func (h *Handler) setupSession(sess *scrierSession, user *vigoauth.User) {
	ctx := context.Background()

	// SSH pubkey auth: resolve OS username, then look up authorized_keys from config.
	var sshAuth *SSHPubkeyAuth
	if sess.protocol == "ssh" && h.configKeys != nil {
		osUsername := user.Username
		scrierAuth, err := h.queries.GetUserScrierAuth(ctx, user.Username)
		if err != nil {
			slog.Warn("scrier username lookup failed", "user", user.Username, "error", err)
		} else if scrierAuth.ScrierUsername != "" {
			osUsername = scrierAuth.ScrierUsername
		}

		authorizedKeys := h.configKeys.UserAuthorizedKeys(osUsername)
		if authorizedKeys != "" {
			ephPriv, ephPub, err := GenerateEphemeralSSHKey()
			if err != nil {
				sess.setStatus(statusError, "failed to generate session key")
				return
			}
			sshAuth = &SSHPubkeyAuth{
				VerifyPubkey:    authorizedKeys,
				EphemeralPubkey: ephPub,
				EphemeralKey:    ephPriv,
				TargetUsername:  osUsername,
			}
		} else {
			slog.Warn("scrier: no authorized_keys found in config for OS user",
				"web_user", user.Username, "os_user", osUsername)
		}
	}

	// Open tunnel (blocks waiting for agent — up to 30s).
	tunnelID, proxyAddr, tunnelDone, err := h.tunnelOpener.OpenTunnel(
		sess.envoyID, sess.hostname, sess.protocol, sess.port, sshAuth)
	if err != nil {
		slog.Warn("scrier tunnel open failed", "session_id", sess.id, "error", err)
		sess.setStatus(statusError, err.Error())
		return
	}

	sess.mu.Lock()
	sess.tunnelID = tunnelID
	sess.tunnelDone = tunnelDone
	sess.mu.Unlock()
	sess.setStatus(statusConnecting, "")

	// Branch by protocol.
	switch sess.protocol {
	case "ssh":
		h.setupSSH(sess, sshAuth, tunnelID, proxyAddr)
	case "rdp", "vnc":
		h.setupGuacd(sess, sshAuth, tunnelID, proxyAddr)
	}

	if st, _ := sess.getStatus(); st == statusError {
		return
	}

	// Record session in DB.
	if h.queries != nil {
		_, _ = h.queries.InsertScrierSession(ctx, db.InsertScrierSessionParams{
			EnvoyID:    sess.envoyID,
			UserID:     user.ID,
			Username:   user.Username,
			Protocol:   sess.protocol,
			TargetPort: int64(sess.port),
			TunnelID:   tunnelID,
		})
	}

	if h.auditWriter != nil {
		_ = h.auditWriter.Record(ctx, "scrier.connect", user.Username, "web_ui",
			fmt.Sprintf("opened %s session to %s (port %d)", sess.protocol, sess.hostname, sess.port), "")
	}

	// Clean up sessions that never get a WebSocket connection or that failed setup.
	time.AfterFunc(60*time.Second, func() {
		st, _ := sess.getStatus()
		switch st {
		case statusReady:
			slog.Info("scrier session expired (no WebSocket connected)", "session_id", sess.id)
			h.cleanupSession(sess)
			sess.setStatus(statusError, "session expired")
			h.removeSession(sess.id)
		case statusError, statusWaiting, statusConnecting:
			slog.Info("scrier session expired (setup incomplete)", "session_id", sess.id, "status", string(st))
			h.cleanupSession(sess)
			h.removeSession(sess.id)
		}
	})
}

// setupSSH connects to the agent's SSH daemon through the tunnel proxy
// using the Go SSH client with ephemeral key authentication.
func (h *Handler) setupSSH(sess *scrierSession, sshAuth *SSHPubkeyAuth, tunnelID, proxyAddr string) {
	// Get SSH username from agent (blocks up to 15s).
	sshUsername := h.tunnelOpener.SSHUsername(tunnelID)
	if sshUsername == "" {
		slog.Warn("scrier: no SSH username from agent", "session_id", sess.id)
		// Fall back to target username from config.
		if sshAuth != nil {
			sshUsername = sshAuth.TargetUsername
		}
		if sshUsername == "" {
			h.tunnelOpener.CloseTunnel(tunnelID)
			sess.setStatus(statusError, "SSH authentication failed — no username")
			return
		}
	}

	sess.mu.Lock()
	sess.sshUsername = sshUsername
	sess.mu.Unlock()

	// Parse the ephemeral private key.
	var authMethods []ssh.AuthMethod
	if sshAuth != nil && len(sshAuth.EphemeralKey) > 0 {
		signer, err := ssh.ParsePrivateKey(sshAuth.EphemeralKey)
		if err != nil {
			slog.Error("scrier: failed to parse ephemeral key", "session_id", sess.id, "error", err)
			h.tunnelOpener.CloseTunnel(tunnelID)
			sess.setStatus(statusError, "SSH key error")
			return
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// Connect to the proxy (which tunnels to the agent's localhost:22).
	sshConfig := &ssh.ClientConfig{
		User:            sshUsername,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", proxyAddr, sshConfig)
	if err != nil {
		slog.Error("scrier SSH dial failed", "session_id", sess.id, "proxy", proxyAddr, "user", sshUsername, "error", err)
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "SSH connection failed")
		return
	}

	session, err := client.NewSession()
	if err != nil {
		slog.Error("scrier SSH session failed", "session_id", sess.id, "error", err)
		client.Close()
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "SSH session failed")
		return
	}

	// Request a PTY.
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		slog.Error("scrier PTY request failed", "session_id", sess.id, "error", err)
		session.Close()
		client.Close()
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "PTY allocation failed")
		return
	}

	ptyIn, err := session.StdinPipe()
	if err != nil {
		slog.Error("scrier stdin pipe failed", "session_id", sess.id, "error", err)
		session.Close()
		client.Close()
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "SSH pipe error")
		return
	}

	ptyOut, err := session.StdoutPipe()
	if err != nil {
		slog.Error("scrier stdout pipe failed", "session_id", sess.id, "error", err)
		session.Close()
		client.Close()
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "SSH pipe error")
		return
	}

	if err := session.Shell(); err != nil {
		slog.Error("scrier shell start failed", "session_id", sess.id, "error", err)
		session.Close()
		client.Close()
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "shell start failed")
		return
	}

	sess.mu.Lock()
	sess.sshClient = client
	sess.sshSession = session
	sess.ptyIn = ptyIn
	sess.ptyOut = ptyOut
	sess.mu.Unlock()

	slog.Info("scrier SSH session ready",
		"session_id", sess.id,
		"tunnel_id", tunnelID,
		"hostname", sess.hostname,
		"proxy", proxyAddr,
		"ssh_user", sshUsername,
	)
	sess.setStatus(statusReady, "")
}

// setupGuacd connects to guacd and performs the Guacamole handshake for RDP and VNC sessions.
func (h *Handler) setupGuacd(sess *scrierSession, sshAuth *SSHPubkeyAuth, tunnelID, proxyAddr string) {
	// Get SSH username from agent if SSH auth was used.
	var sshUsername string
	var sshPrivateKeyPEM []byte
	if sshAuth != nil {
		sshUsername = h.tunnelOpener.SSHUsername(tunnelID)
		sshPrivateKeyPEM = sshAuth.EphemeralKey
	}

	sess.mu.Lock()
	sess.sshUsername = sshUsername
	sess.mu.Unlock()

	// Connect to guacd.
	guacdConn, err := net.DialTimeout("tcp", h.tunnelOpener.GuacdAddr(), 5*time.Second)
	if err != nil {
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "cannot reach guacd")
		return
	}

	proxyHost, proxyPort, _ := net.SplitHostPort(proxyAddr)

	guacdRW := bufio.NewReadWriter(
		bufio.NewReaderSize(guacdConn, 64*1024),
		bufio.NewWriter(guacdConn),
	)
	connID, err := doGuacdHandshake(guacdRW, sess.protocol, proxyHost, proxyPort, sshUsername, sshPrivateKeyPEM, "", sess.width, sess.height)
	if err != nil {
		slog.Error("scrier guacd handshake failed", "session_id", sess.id, "error", err)
		guacdConn.Close()
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "guacd connection failed")
		return
	}

	sess.mu.Lock()
	sess.guacdConn = guacdConn
	sess.guacdRW = guacdRW
	sess.guacdConnID = connID
	sess.sshPrivateKey = sshPrivateKeyPEM
	sess.mu.Unlock()

	slog.Info("scrier guacd session ready",
		"session_id", sess.id,
		"tunnel_id", tunnelID,
		"hostname", sess.hostname,
		"proxy", proxyAddr,
		"connection_id", connID,
	)
	sess.setStatus(statusReady, "")
}

// --- Phase 1b: Status polling ---

// HandleSessionStatus returns the current setup status of a Scrier session.
func (h *Handler) HandleSessionStatus(w http.ResponseWriter, r *http.Request) {
	user := vigoauth.UserFromContext(r.Context())
	if user == nil || !user.IsAdmin() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	sessionID := r.PathValue("id")
	sess := h.getSession(sessionID)
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	if sess.user != user.Username {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your session"})
		return
	}

	st, errMsg := sess.getStatus()
	resp := map[string]string{"status": string(st)}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- Phase 2: WebSocket relay ---

// HandleRelay upgrades to WebSocket and relays between the browser and
// the backend (SSH PTY for SSH sessions, guacd for RDP sessions).
func (h *Handler) HandleRelay(w http.ResponseWriter, r *http.Request) {
	user := vigoauth.UserFromContext(r.Context())
	if user == nil || !user.IsAdmin() {
		http.Error(w, "admin access required", http.StatusForbidden)
		return
	}

	sessionID := r.PathValue("id")
	sess := h.getSession(sessionID)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if sess.user != user.Username {
		http.Error(w, "not your session", http.StatusForbidden)
		return
	}

	st, errMsg := sess.getStatus()
	if st != statusReady {
		http.Error(w, fmt.Sprintf("session not ready: %s %s", st, errMsg), http.StatusConflict)
		return
	}

	sess.setStatus("relaying", "")

	// Accept WebSocket — SSH uses no subprotocol, RDP/VNC use "guacamole".
	opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
	if sess.protocol == "rdp" || sess.protocol == "vnc" {
		opts.Subprotocols = []string{"guacamole"}
	}
	wsConn, err := websocket.Accept(w, r, opts)
	if err != nil {
		slog.Error("scrier websocket upgrade failed", "session_id", sessionID, "error", err)
		return
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "session ended")

	startedAt := time.Now()

	slog.Info("scrier websocket relay started",
		"session_id", sessionID,
		"tunnel_id", sess.tunnelID,
		"hostname", sess.hostname,
		"protocol", sess.protocol,
		"user", user.Username,
	)

	switch sess.protocol {
	case "ssh":
		h.relaySSH(wsConn, sess, sessionID)
	case "rdp", "vnc":
		h.relayGuacd(wsConn, sess, sessionID)
	}

	// Cleanup.
	h.cleanupSession(sess)
	h.removeSession(sessionID)

	duration := time.Since(startedAt).Round(time.Second)

	if h.queries != nil {
		_ = h.queries.EndScrierSession(context.Background(), db.EndScrierSessionParams{
			Status:   "closed",
			TunnelID: sess.tunnelID,
		})
	}

	if h.auditWriter != nil {
		_ = h.auditWriter.Record(context.Background(), "scrier.disconnect", user.Username, "web_ui",
			fmt.Sprintf("closed %s session to %s after %s", sess.protocol, sess.hostname, duration), "")
	}

	slog.Info("scrier websocket relay ended",
		"session_id", sessionID,
		"hostname", sess.hostname,
		"user", user.Username,
		"duration", duration,
	)
}

// relaySSH relays raw PTY bytes between the WebSocket and the SSH session.
// Binary WebSocket messages carry terminal data; text messages carry JSON
// control commands (resize).
func (h *Handler) relaySSH(wsConn *websocket.Conn, sess *scrierSession, sessionID string) {
	bgCtx := context.Background()
	wsConn.SetReadLimit(64 * 1024) // terminal data is small

	done := make(chan struct{}, 1)

	// PTY → WebSocket: read terminal output and send as binary messages.
	go func() {
		defer func() {
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		buf := make([]byte, 32*1024)
		for {
			n, err := sess.ptyOut.Read(buf)
			if n > 0 {
				wCtx, cancel := context.WithTimeout(bgCtx, 5*time.Second)
				wErr := wsConn.Write(wCtx, websocket.MessageBinary, buf[:n])
				cancel()
				if wErr != nil {
					slog.Info("scrier ssh ws write error", "session_id", sessionID, "error", wErr)
					return
				}
			}
			if err != nil {
				slog.Info("scrier ssh pty read done", "session_id", sessionID, "error", err)
				return
			}
		}
	}()

	// WebSocket → PTY: read input and control messages.
	go func() {
		defer func() {
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		for {
			msgType, data, err := wsConn.Read(bgCtx)
			if err != nil {
				slog.Info("scrier ssh ws read done", "session_id", sessionID, "error", err)
				return
			}
			switch msgType {
			case websocket.MessageBinary:
				// Terminal input.
				if _, wErr := sess.ptyIn.Write(data); wErr != nil {
					slog.Info("scrier ssh pty write error", "session_id", sessionID, "error", wErr)
					return
				}
			case websocket.MessageText:
				// JSON control message.
				var ctrl struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" && ctrl.Cols > 0 && ctrl.Rows > 0 {
					_ = sess.sshSession.WindowChange(ctrl.Rows, ctrl.Cols)
				}
			}
		}
	}()

	// Wait for either side to close.
	select {
	case <-done:
	case <-sess.tunnelDone:
	}
}

// relayGuacd relays Guacamole protocol between the WebSocket and guacd (RDP).
func (h *Handler) relayGuacd(wsConn *websocket.Conn, sess *scrierSession, sessionID string) {
	bgCtx := context.Background()
	wsConn.SetReadLimit(1 << 20) // 1 MiB — RDP frames can be large

	// Buffered channel for guacd→ws writes with backpressure.
	wsCh := make(chan []byte, 256)

	wsWriteDone := make(chan struct{})
	go func() {
		defer close(wsWriteDone)
		for data := range wsCh {
			wCtx, wCancel := context.WithTimeout(bgCtx, 5*time.Second)
			err := wsConn.Write(wCtx, websocket.MessageText, data)
			wCancel()
			if err != nil {
				slog.Info("scrier rdp ws write error", "session_id", sessionID, "error", err)
				return
			}
		}
	}()

	enqueueWS := func(data []byte) {
		select {
		case wsCh <- data:
		case <-sess.tunnelDone:
		case <-wsWriteDone:
		}
	}

	// Send guacd connection UUID as the first message.
	if sess.guacdConnID != "" {
		uuidInst := fmt.Sprintf("0.,%d.%s;", len(sess.guacdConnID), sess.guacdConnID)
		uCtx, uCancel := context.WithTimeout(bgCtx, 5*time.Second)
		err := wsConn.Write(uCtx, websocket.MessageText, []byte(uuidInst))
		uCancel()
		if err != nil {
			slog.Error("scrier failed to send connection UUID", "session_id", sessionID, "error", err)
			return
		}
	}

	wsDone := make(chan struct{})
	var injectedTS string

	// guacd → WebSocket: relay display instructions with instruction-aligned messages.
	go func() {
		buf := make([]byte, 256*1024)
		var carry []byte
		syncInjected := false
		for {
			n, err := sess.guacdRW.Read(buf)
			if n > 0 {
				var data []byte
				if len(carry) > 0 {
					data = make([]byte, len(carry)+n)
					copy(data, carry)
					copy(data[len(carry):], buf[:n])
					carry = nil
				} else {
					data = make([]byte, n)
					copy(data, buf[:n])
				}

				lastSemi := bytes.LastIndexByte(data, ';')
				if lastSemi >= 0 {
					enqueueWS(data[:lastSemi+1])
					if lastSemi+1 < len(data) {
						carry = make([]byte, len(data)-lastSemi-1)
						copy(carry, data[lastSemi+1:])
					}
				} else {
					carry = data
				}

				if !syncInjected && lastSemi >= 0 {
					injectedTS = fmt.Sprintf("%d", time.Now().UnixMilli())
					syncInst := fmt.Sprintf("4.sync,%d.%s;", len(injectedTS), injectedTS)
					enqueueWS([]byte(syncInst))
					syncInjected = true
				}
			}
			if err != nil {
				if len(carry) > 0 {
					enqueueWS(carry)
				}
				return
			}
		}
	}()

	// WebSocket → guacd: relay input, handle pings.
	go func() {
		defer close(wsDone)
		for {
			_, data, err := wsConn.Read(bgCtx)
			if err != nil {
				return
			}
			msg := string(data)
			if pong, isPing := tunnelPong(msg); isPing {
				enqueueWS([]byte(pong))
				continue
			}
			if injectedTS != "" && msg == fmt.Sprintf("4.sync,%d.%s;", len(injectedTS), injectedTS) {
				injectedTS = ""
				continue
			}
			if _, err := sess.guacdConn.Write(data); err != nil {
				return
			}
		}
	}()

	select {
	case <-wsDone:
	case <-sess.tunnelDone:
	case <-wsWriteDone:
	}

	close(wsCh)
}

// --- Helpers ---

// cleanupSession closes protocol-specific resources and the tunnel.
func (h *Handler) cleanupSession(sess *scrierSession) {
	if sess.sshSession != nil {
		sess.sshSession.Close()
	}
	if sess.sshClient != nil {
		sess.sshClient.Close()
	}
	if sess.guacdConn != nil {
		sess.guacdConn.Close()
	}
	h.tunnelOpener.CloseTunnel(sess.tunnelID)
}

func (h *Handler) getSession(id string) *scrierSession {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	return h.sessions[id]
}

func (h *Handler) removeSession(id string) {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	delete(h.sessions, id)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func autoSelectProtocol(traits map[string]string) (protocol string, port uint32) {
	family := traits["os.family"]
	switch {
	case strings.EqualFold(family, "windows"):
		return "rdp", 3389
	default:
		return "ssh", 22
	}
}
