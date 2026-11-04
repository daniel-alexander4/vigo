// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/httpsec"
	"git.alexander4.io/vigo/server/metrics"
	"github.com/google/uuid"
)

// staticFS holds the session pages' CSS and JS, served at /scrier/assets/.
// External assets are what let the pages run under the locked CSP.
//
//go:embed static
var staticFS embed.FS

// SSHPubkeyAuth holds the SSH pubkey auth parameters for a Scrier session.
// The agent scans every regular user's authorized_keys for VerifyPubkey to
// determine which OS user to land in — there is no operator-supplied target.
type SSHPubkeyAuth struct {
	VerifyPubkey    string // web user's SSH pubkey
	EphemeralPubkey string // per-session pubkey installed temporarily into the matched user's authorized_keys
	EphemeralKey    []byte // PEM-encoded ephemeral private key
}

// TunnelOpener is the interface the WebSocket handler uses to create tunnels.
type TunnelOpener interface {
	OpenTunnel(envoyID, hostname, protocol string, targetPort uint32, sshAuth *SSHPubkeyAuth) (tunnelID string, proxyAddr string, done chan struct{}, err error)
	HasAgentStream(envoyID string) bool
	CloseTunnel(tunnelID string)
	ActiveSessions() int
	GuacdAddr() string
	// SSHResult blocks for the agent's TunnelOpenResult and returns the OS
	// username it landed in plus any failure reason it reported. Empty
	// username with non-empty errorMessage means the agent rejected the
	// auth; empty/empty means timeout.
	SSHResult(tunnelID string) (username, errorMessage string)
}

// AuditRecorder writes tamper-evident audit entries.
type AuditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// --- Session store ---

type sessionStatus string

const (
	statusWaiting    sessionStatus = "waiting"
	statusConnecting sessionStatus = "connecting"
	statusReady      sessionStatus = "ready"
	statusRelaying   sessionStatus = "relaying"
	statusError      sessionStatus = "error"
)

// scrierSession holds state for a single Scrier connection setup.
type scrierSession struct {
	mu              sync.Mutex
	id              string
	envoyID         string
	hostname        string
	protocol        string
	port            uint32
	user            string // web UI username
	requestedPubkey string // pubkey shipped in connect request (CLI path); empty on browser path
	status          sessionStatus
	errorMsg        string
	createdAt       time.Time

	// Set during setup:
	tunnelID    string
	tunnelDone  chan struct{}
	sshUsername string
	recordUser  string // webuser the session's DB row + audit entries name

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

	// Lifecycle: who owns teardown — the 60s expiry AfterFunc or the
	// WebSocket relay handler. The first to claim() wins; the other
	// no-ops. cleanupOnce makes cleanupSession itself idempotent so
	// even an accidental second call (defense in depth) is safe.
	claimed     bool
	cleanupOnce sync.Once
}

// claim atomically marks this session as taken — for relay OR for
// expiry-time cleanup. Returns true iff this caller is the first to
// claim; subsequent calls return false. Lets the WS relay handler and
// the session-expiry timer race safely: whichever lands first owns
// teardown, the other no-ops.
func (s *scrierSession) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return false
	}
	s.claimed = true
	return true
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
	auditWriter  AuditRecorder
	metrics      *metrics.Metrics
	version      string
	connectWait  string // human-readable max wait (check-in interval + 20%)
	tmplSSH      *template.Template
	tmplRDP      *template.Template

	// sessionExpiry is how long a session can sit between setupSession
	// returning ready and the WebSocket actually connecting. Tests
	// override this to a few milliseconds so the expiry/claim race is
	// exercisable without sleeping 60s.
	sessionExpiry time.Duration

	sessionsMu sync.Mutex
	sessions   map[string]*scrierSession
}

// defaultSessionExpiry — production timeout for "session ready but no
// WebSocket connected." Tests override via Handler.sessionExpiry.
const defaultSessionExpiry = 60 * time.Second

// NewHandler creates a Scrier handler.
func NewHandler(opener TunnelOpener, fleetIndex *fleet.FleetIndex, queries *db.Queries, version string, checkinInterval, offlineThreshold time.Duration) *Handler {
	tmplSSH := template.Must(template.New("scrier-ssh").Parse(scrierSessionSSHHTML))
	tmplRDP := template.Must(template.New("scrier-rdp").Parse(scrierSessionRDPHTML))

	// Compute max wait: check-in interval + 20% jitter.
	maxWait := checkinInterval + checkinInterval/5
	waitStr := formatDuration(maxWait)

	return &Handler{
		tunnelOpener:  opener,
		fleetIndex:    fleetIndex,
		queries:       queries,
		version:       version,
		connectWait:   waitStr,
		tmplSSH:       tmplSSH,
		tmplRDP:       tmplRDP,
		sessionExpiry: defaultSessionExpiry,
		sessions:      make(map[string]*scrierSession),
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

// SetMetrics attaches the Prometheus collectors; the scrier record-error
// counter lives there.
func (h *Handler) SetMetrics(m *metrics.Metrics) {
	h.metrics = m
}

// recordFailed logs and counts a failed write to the remote-session record
// (DB session row or audit entry). The session carries on: a recording
// failure must be visible, but tearing down a live admin shell over it
// would not restore the record.
func (h *Handler) recordFailed(op, sessionID string, err error) {
	slog.Error("scrier: session record write failed", "op", op, "session_id", sessionID, "error", err)
	if h.metrics != nil {
		h.metrics.ScrierRecordErrors.WithLabelValues(op).Inc()
	}
}

// RegisterRoutes registers the Scrier endpoints.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /scrier/{id}", h.HandleSession)
	mux.HandleFunc("POST /api/v1/scrier/connect", h.HandleConnect)
	mux.HandleFunc("GET /api/v1/scrier/sessions/{id}/status", h.HandleSessionStatus)
	mux.HandleFunc("GET /ws/scrier/{id}", h.HandleRelay)
	mux.HandleFunc("GET /scrier/assets/{name}", h.HandleAsset)
}

// HandleAsset serves the session pages' embedded CSS/JS. It sits behind
// the auth middleware like the pages themselves.
func (h *Handler) HandleAsset(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "assets unavailable", http.StatusInternalServerError)
		return
	}
	name := r.PathValue("name")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	httpsec.Headers(w)
	http.ServeFileFS(w, r, sub, name)
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
		// xterm.js injects runtime <style> elements; see ScrierTerminalCSP.
		httpsec.HeadersWithCSP(w, httpsec.ScrierTerminalCSP)
		if err := h.tmplSSH.Execute(w, data); err != nil {
			slog.Error("scrier: session template render failed", "protocol", protocol, "error", err)
		}
	default:
		// RDP and VNC both use the Guacamole template.
		httpsec.Headers(w)
		if err := h.tmplRDP.Execute(w, data); err != nil {
			slog.Error("scrier: session template render failed", "protocol", protocol, "error", err)
		}
	}
}

// --- Phase 1: Connect (setup) ---

type connectRequest struct {
	EnvoyID  string `json:"envoy_id"`
	Protocol string `json:"protocol"`
	Port     uint32 `json:"port"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	// SSHPublicKey is the pubkey the operator's CLI auto-detects per
	// session (cli/scrier_ssh.go::resolveOperatorPubkey). It is never
	// trusted on its own: it must equal the key stored on the caller's
	// webuser row, or — for the local-admin token — be the stored key of
	// exactly one admin webuser (see [resolveVerifyPubkey]). Nothing is
	// written back. The browser path omits it and uses the stored key.
	SSHPublicKey string `json:"ssh_public_key,omitempty"`
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
		id:              uuid.New().String(),
		envoyID:         req.EnvoyID,
		hostname:        envoy.Hostname,
		protocol:        req.Protocol,
		port:            req.Port,
		width:           width,
		height:          height,
		user:            user.Username,
		requestedPubkey: strings.TrimSpace(req.SSHPublicKey),
		status:          statusWaiting,
		createdAt:       time.Now(),
	}

	h.sessionsMu.Lock()
	h.sessions[sess.id] = sess
	h.sessionsMu.Unlock()

	daemon.SafeGo("scrier-setup-session", func() { h.setupSession(sess, user) })

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

	// Every session leaves the map: armed here, before any early return,
	// so a session whose setup fails is removed once the poller has had
	// the expiry window to read its error.
	defer h.armExpiry(sess)

	// Who the session is recorded as. For SSH it is the webuser whose
	// stored key the agent verifies against (see [resolveVerifyPubkey]);
	// RDP/VNC carry no key and record the caller.
	recordUserID, recordUsername := user.ID, user.Username

	// SSH pubkey auth: the verify key is the one stored on a webuser row,
	// read from the DB at connect time. The agent scans every regular
	// user's authorized_keys for it and lands the operator in whichever OS
	// user owns the match. A key in the connect request (CLI path) must
	// match the stored key; it is never used on its own.
	var sshAuth *SSHPubkeyAuth
	if sess.protocol == "ssh" {
		var store sshPubkeyStore
		if h.queries != nil {
			store = h.queries
		}
		ident, err := resolveVerifyPubkey(ctx, store, user, sess.requestedPubkey)
		if err != nil {
			sess.setStatus(statusError, err.Error())
			return
		}
		verifyPubkey := ident.Pubkey
		recordUserID, recordUsername = ident.UserID, ident.Username
		if verifyPubkey == "" {
			sess.setStatus(statusError,
				"no SSH pubkey available — for browser scrier, run `vigocli webusers set-role --username <yourname> --role admin` (existing user) or `vigocli webusers create --username <yourname> --role admin` (new user) from the server host; both seed your DB-side pubkey from your OS user's ~/.ssh/id_*.pub. For CLI scrier, the CLI auto-detects and sends inline.")
			return
		}
		ephPriv, ephPub, err := GenerateEphemeralSSHKey()
		if err != nil {
			sess.setStatus(statusError, "failed to generate session key")
			return
		}
		sshAuth = &SSHPubkeyAuth{
			VerifyPubkey:    verifyPubkey,
			EphemeralPubkey: ephPub,
			EphemeralKey:    ephPriv,
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
		h.setupGuacd(sess, tunnelID, proxyAddr)
	}

	if st, _ := sess.getStatus(); st == statusError {
		return
	}

	// Record session in DB.
	sess.mu.Lock()
	sess.recordUser = recordUsername
	sess.mu.Unlock()

	if h.queries != nil {
		if _, err := h.queries.InsertScrierSession(ctx, db.InsertScrierSessionParams{
			EnvoyID:    sess.envoyID,
			UserID:     recordUserID,
			Username:   recordUsername,
			Protocol:   sess.protocol,
			TargetPort: int64(sess.port),
			TunnelID:   tunnelID,
		}); err != nil {
			h.recordFailed("session_insert", sess.id, err)
		}
	}

	if h.auditWriter != nil {
		if err := h.auditWriter.Record(ctx, "scrier.connect", recordUsername, auditSource(user),
			fmt.Sprintf("opened %s session to %s (port %d)", sess.protocol, sess.hostname, sess.port), ""); err != nil {
			h.recordFailed("audit_connect", sess.id, err)
		}
	}
}

// auditSource names the caller's credential in scrier audit entries: the
// local-admin token is root on the server host acting for the webuser the
// session is attributed to.
func auditSource(user *vigoauth.User) string {
	if user.ID == vigoauth.LocalAdminUserID {
		return "local_admin"
	}
	return "web_ui"
}

// armExpiry schedules the session's removal from h.sessions. It covers
// sessions that never get a WebSocket connection and sessions whose setup
// failed (kept for one expiry window so the status poller can read the
// error, then dropped). The claim flag is the single SoT for "who owns
// teardown": if HandleRelay already claimed, this no-ops; if we claim,
// the relay handler sees claimed=true at its entry check and responds
// 409 Conflict rather than relay against resources we're closing.
func (h *Handler) armExpiry(sess *scrierSession) {
	time.AfterFunc(h.sessionExpiry, func() {
		defer daemon.Recover("scrier-session-expiry", nil)
		if !sess.claim() {
			return
		}
		st, _ := sess.getStatus()
		slog.Info("scrier session expired", "session_id", sess.id, "status", string(st))
		h.cleanupSession(sess)
		if st == statusReady {
			sess.setStatus(statusError, "session expired")
		}
		h.removeSession(sess.id)
	})
}

// setupSSH connects to the agent's SSH daemon through the tunnel proxy
// using the Go SSH client with ephemeral key authentication.
func (h *Handler) setupSSH(sess *scrierSession, sshAuth *SSHPubkeyAuth, tunnelID, proxyAddr string) {
	// Block up to 15s for the agent's TunnelOpenResult. The agent either
	// reports the OS username it landed in (success) or an explanation
	// (e.g., pubkey not in any local authorized_keys).
	sshUsername, agentErr := h.tunnelOpener.SSHResult(tunnelID)
	if sshUsername == "" {
		msg := agentErr
		if msg == "" {
			msg = "SSH pubkey not authorized on this envoy"
		}
		slog.Warn("scrier: SSH auth failed",
			"session_id", sess.id,
			"agent_error", agentErr,
		)
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, msg)
		return
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
	//
	// HostKeyCallback intentionally accepts any server key. The SSH connection
	// rides through vigosrv into an AgentStream-multiplexed TunnelStream and
	// terminates at the agent's `127.0.0.1:<sshd-port>` after the agent has
	// planted a per-session ephemeral ED25519 key in the matched OS user's
	// `authorized_keys`. There is no network MITM surface: the bytes never
	// leave the agent-host loopback once they exit the mTLS tunnel, and the
	// auth is the per-session keypair, not a long-lived host key. A
	// host-key-pinning callback here would gate nothing and only complicate
	// the first-session UX.
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
// These protocols don't carry SSH auth — guacd handles their own auth — so no
// authorized_keys interaction is needed on the agent side.
func (h *Handler) setupGuacd(sess *scrierSession, tunnelID, proxyAddr string) {
	var sshUsername string
	var sshPrivateKeyPEM []byte

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

	// Take ownership of teardown before doing anything user-visible.
	// If the session-expiry timer fired and claimed first, we'd be
	// racing into a relay loop against resources it's already closing
	// — short-circuit with 409 instead.
	if !sess.claim() {
		http.Error(w, "session already claimed (expired or in use)", http.StatusConflict)
		return
	}

	sess.setStatus(statusRelaying, "")

	// Accept WebSocket — SSH uses no subprotocol, RDP/VNC use "guacamole".
	// The library's default Origin check stays on: a browser upgrade must
	// carry an Origin whose host is this request's Host (the scrier page
	// is served by this same listener), which is what stops a foreign page
	// riding the admin's session cookie into a shell. A request with no
	// Origin (vigocli) is not a browser and passes.
	opts := &websocket.AcceptOptions{}
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
		if err := h.queries.EndScrierSession(context.Background(), db.EndScrierSessionParams{
			Status:   "closed",
			TunnelID: sess.tunnelID,
		}); err != nil {
			h.recordFailed("session_end", sessionID, err)
		}
	}

	if h.auditWriter != nil {
		sess.mu.Lock()
		actor := sess.recordUser
		sess.mu.Unlock()
		if actor == "" {
			actor = user.Username
		}
		if err := h.auditWriter.Record(context.Background(), "scrier.disconnect", actor, auditSource(user),
			fmt.Sprintf("closed %s session to %s after %s", sess.protocol, sess.hostname, duration), ""); err != nil {
			h.recordFailed("audit_disconnect", sessionID, err)
		}
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
	// Cancellable context: each goroutine defer-cancels, so whichever
	// reader exits first wakes the WS-side wsConn.Read(ctx) on the
	// partner goroutine immediately — no waiting for cleanupSession's
	// underlying-conn-close to propagate. PTY-side reads can't observe
	// ctx, so that direction still relies on cleanupSession closing the
	// SSH client (called by HandleRelay right after this returns).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsConn.SetReadLimit(64 * 1024) // terminal data is small

	done := make(chan struct{}, 1)

	// PTY → WebSocket: read terminal output and send as binary messages.
	go func() {
		defer daemon.Recover("scrier-pty-to-ws", nil)
		defer func() {
			cancel()
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		buf := make([]byte, 32*1024)
		for {
			n, err := sess.ptyOut.Read(buf)
			if n > 0 {
				wCtx, wCancel := context.WithTimeout(ctx, 5*time.Second)
				wErr := wsConn.Write(wCtx, websocket.MessageBinary, buf[:n])
				wCancel()
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
		defer daemon.Recover("scrier-ws-to-pty", nil)
		defer func() {
			cancel()
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		for {
			msgType, data, err := wsConn.Read(ctx)
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
	// Cancellable context for the same reason as relaySSH — gives the
	// WS-read side a fast wakeup path independent of cleanupSession's
	// Close chain. The guacd-side reader can't observe ctx and still
	// depends on guacdConn.Close from cleanupSession after this returns.
	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wsConn.SetReadLimit(1 << 20) // 1 MiB — RDP frames can be large

	// Buffered channel for guacd→ws writes with backpressure.
	wsCh := make(chan []byte, 256)

	wsWriteDone := make(chan struct{})
	go func() {
		defer daemon.Recover("scrier-guacd-ws-write", nil)
		defer close(wsWriteDone)
		for {
			select {
			case data := <-wsCh:
				wCtx, wCancel := context.WithTimeout(bgCtx, 5*time.Second)
				err := wsConn.Write(wCtx, websocket.MessageText, data)
				wCancel()
				if err != nil {
					slog.Info("scrier rdp ws write error", "session_id", sessionID, "error", err)
					return
				}
			case <-bgCtx.Done():
				return
			}
		}
	}()

	enqueueWS := func(data []byte) {
		select {
		case wsCh <- data:
		case <-sess.tunnelDone:
		case <-wsWriteDone:
		case <-bgCtx.Done():
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
		defer daemon.Recover("scrier-guacd-to-ws", nil)
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
		defer daemon.Recover("scrier-ws-to-guacd", nil)
		defer cancel()
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
	// No close(wsCh): the WS-writer exits on bgCtx.Done() (via the
	// deferred cancel above), not on channel close. Closing wsCh while
	// the guacd-reader might still call enqueueWS was a send-on-closed
	// panic risk; ctx-driven shutdown side-steps it.
}

// --- Helpers ---

// cleanupSession closes protocol-specific resources and the tunnel.
// Idempotent via sess.cleanupOnce — the claim flag prevents the relay
// handler and the expiry timer from both reaching cleanup, but Once
// adds a second layer in case a future caller (or a refactor) lands
// without consulting the claim.
func (h *Handler) cleanupSession(sess *scrierSession) {
	sess.cleanupOnce.Do(func() {
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
	})
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
