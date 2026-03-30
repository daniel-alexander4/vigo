// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package scrier implements the Vigo Scrier remote access feature.
// Two-phase architecture:
//   Phase 1 (HTTP): POST /api/v1/scrier/connect → background setup (tunnel + guacd)
//   Phase 2 (WebSocket): GET /ws/scrier/{session_id} → pure relay, no setup
package scrier

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/google/uuid"
)

// SSHPubkeyAuth holds the SSH pubkey auth parameters for a Scrier session.
type SSHPubkeyAuth struct {
	VerifyPubkey    string // web user's SSH pubkey for authorized_keys check
	EphemeralPubkey string // per-session pubkey installed temporarily
	EphemeralKey    []byte // PEM-encoded ephemeral private key (for guacd)
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

// UserPubkeyLookup retrieves a user's Scrier auth by web username.
type UserPubkeyLookup interface {
	GetUserScrierAuth(ctx context.Context, username string) (db.GetUserScrierAuthRow, error)
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
	tunnelID      string
	tunnelDone    chan struct{}
	guacdConn     net.Conn
	guacdRW       *bufio.ReadWriter
	sshUsername   string
	sshPrivateKey []byte
}

func (s *scrierSession) setStatus(st sessionStatus, errMsg string) {
	s.mu.Lock()
	s.status = st
	s.errorMsg = errMsg
	s.mu.Unlock()
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
	pubkeyLookup UserPubkeyLookup
	auditWriter  AuditRecorder
	version      string
	tmplSession  *template.Template

	sessionsMu sync.Mutex
	sessions   map[string]*scrierSession
}

// NewHandler creates a Scrier handler.
func NewHandler(opener TunnelOpener, fleetIndex *fleet.FleetIndex, queries *db.Queries, pubkeys UserPubkeyLookup, version string) *Handler {
	tmpl := template.Must(template.New("scrier-session").Parse(scrierSessionHTML))
	return &Handler{
		tunnelOpener: opener,
		fleetIndex:   fleetIndex,
		queries:      queries,
		pubkeyLookup: pubkeys,
		version:      version,
		tmplSession:  tmpl,
		sessions:     make(map[string]*scrierSession),
	}
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

// --- Phase 1: Connect (setup) ---

type connectRequest struct {
	EnvoyID  string `json:"envoy_id"`
	Protocol string `json:"protocol"`
	Port     uint32 `json:"port"`
}

// HandleConnect starts a Scrier session setup in the background.
// Returns a session ID that the browser polls for status.
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

	if req.Protocol != "ssh" && req.Protocol != "rdp" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "protocol must be ssh or rdp"})
		return
	}
	if req.Port == 0 || req.Port > 65535 {
		if req.Protocol == "rdp" {
			req.Port = 3389
		} else {
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

	sess := &scrierSession{
		id:        uuid.New().String(),
		envoyID:   req.EnvoyID,
		hostname:  envoy.Hostname,
		protocol:  req.Protocol,
		port:      req.Port,
		user:      user.Username,
		status:    statusWaiting,
		createdAt: time.Now(),
	}

	h.sessionsMu.Lock()
	h.sessions[sess.id] = sess
	h.sessionsMu.Unlock()

	// Background setup.
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
// agent, connects to guacd, does the handshake, and marks the session ready.
func (h *Handler) setupSession(sess *scrierSession, user *vigoauth.User) {
	ctx := context.Background()

	// SSH pubkey auth.
	var sshAuth *SSHPubkeyAuth
	if sess.protocol == "ssh" && h.pubkeyLookup != nil {
		scrierAuth, err := h.pubkeyLookup.GetUserScrierAuth(ctx, user.Username)
		if err != nil {
			slog.Warn("scrier auth lookup failed", "user", user.Username, "error", err)
		}
		if scrierAuth.SshPublicKey != "" && scrierAuth.ScrierUsername != "" {
			ephPriv, ephPub, err := GenerateEphemeralSSHKey()
			if err != nil {
				sess.setStatus(statusError, "failed to generate session key")
				return
			}
			sshAuth = &SSHPubkeyAuth{
				VerifyPubkey:    scrierAuth.SshPublicKey,
				EphemeralPubkey: ephPub,
				EphemeralKey:    ephPriv,
				TargetUsername:  scrierAuth.ScrierUsername,
			}
		}
	}

	// Open tunnel (blocks waiting for agent — up to 30s).
	tunnelID, proxyAddr, tunnelDone, err := h.tunnelOpener.OpenTunnel(
		sess.envoyID, sess.hostname, sess.protocol, sess.port, sshAuth)
	if err != nil {
		slog.Warn("scrier tunnel open failed", "session_id", sess.id, "error", err)
		sess.setStatus(statusError, "failed to open tunnel to agent")
		return
	}

	sess.mu.Lock()
	sess.tunnelID = tunnelID
	sess.tunnelDone = tunnelDone
	sess.mu.Unlock()
	sess.setStatus(statusConnecting, "")

	// Connect to guacd.
	guacdConn, err := net.DialTimeout("tcp", h.tunnelOpener.GuacdAddr(), 5*time.Second)
	if err != nil {
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "cannot reach guacd")
		return
	}

	proxyHost, proxyPort, _ := net.SplitHostPort(proxyAddr)

	// Get SSH username from agent.
	var sshUsername string
	var sshPrivateKeyPEM []byte
	if sshAuth != nil {
		sshUsername = h.tunnelOpener.SSHUsername(tunnelID)
		sshPrivateKeyPEM = sshAuth.EphemeralKey
	}

	// Guacd handshake.
	guacdRW := bufio.NewReadWriter(
		bufio.NewReaderSize(guacdConn, 64*1024),
		bufio.NewWriter(guacdConn),
	)
	connID, err := doGuacdHandshake(guacdRW, sess.protocol, proxyHost, proxyPort, sshUsername, sshPrivateKeyPEM)
	if err != nil {
		slog.Error("scrier guacd handshake failed", "session_id", sess.id, "error", err)
		guacdConn.Close()
		h.tunnelOpener.CloseTunnel(tunnelID)
		sess.setStatus(statusError, "guacd connection failed")
		return
	}

	slog.Info("scrier session ready",
		"session_id", sess.id,
		"tunnel_id", tunnelID,
		"hostname", sess.hostname,
		"protocol", sess.protocol,
		"proxy", proxyAddr,
		"connection_id", connID,
		"ssh_user", sshUsername,
	)

	// Store guacd connection for the relay.
	sess.mu.Lock()
	sess.guacdConn = guacdConn
	sess.guacdRW = guacdRW
	sess.sshUsername = sshUsername
	sess.sshPrivateKey = sshPrivateKeyPEM
	sess.mu.Unlock()
	sess.setStatus(statusReady, "")

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

	// Audit.
	if h.auditWriter != nil {
		_ = h.auditWriter.Record(ctx, "scrier.connect", user.Username, "web_ui",
			fmt.Sprintf("opened %s session to %s (port %d)", sess.protocol, sess.hostname, sess.port), "")
	}

	// Wait for WebSocket to connect or timeout.
	// If no WebSocket connects within 60s of ready, clean up.
	time.AfterFunc(60*time.Second, func() {
		st, _ := sess.getStatus()
		if st == statusReady {
			slog.Info("scrier session expired (no WebSocket connected)", "session_id", sess.id)
			guacdConn.Close()
			h.tunnelOpener.CloseTunnel(tunnelID)
			sess.setStatus(statusError, "session expired")
			h.removeSession(sess.id)
		}
	})
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

	// Verify requesting user owns this session.
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

// HandleRelay upgrades to WebSocket and relays between the browser and guacd.
// Only works when the session status is "ready".
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

	// Claim the session — mark as in-use so the 60s cleanup timer doesn't fire.
	sess.setStatus("relaying", "")

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("scrier websocket upgrade failed", "session_id", sessionID, "error", err)
		return
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "session ended")

	ctx := context.Background()
	startedAt := time.Now()

	slog.Info("scrier websocket relay started",
		"session_id", sessionID,
		"tunnel_id", sess.tunnelID,
		"hostname", sess.hostname,
		"protocol", sess.protocol,
		"user", user.Username,
	)

	var wsMu sync.Mutex
	writeWS := func(data []byte) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return wsConn.Write(ctx, websocket.MessageText, data)
	}

	wsDone := make(chan struct{})

	// guacd → WebSocket: relay display instructions.
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := sess.guacdRW.Read(buf)
			if n > 0 {
				if writeErr := writeWS(buf[:n]); writeErr != nil {
					slog.Info("scrier ws write error", "session_id", sessionID, "error", writeErr)
					return
				}
			}
			if err != nil {
				slog.Info("scrier guacd read done", "session_id", sessionID, "error", err)
				return
			}
		}
	}()

	// WebSocket → guacd: relay input, handle pings.
	go func() {
		defer close(wsDone)
		for {
			_, data, err := wsConn.Read(ctx)
			if err != nil {
				slog.Info("scrier ws read done", "session_id", sessionID, "error", err)
				return
			}

			if pong, isPing := tunnelPong(string(data)); isPing {
				_ = writeWS([]byte(pong))
				continue
			}

			if _, err := sess.guacdConn.Write(data); err != nil {
				slog.Info("scrier guacd write error", "session_id", sessionID, "error", err)
				return
			}
		}
	}()

	// Wait for either side to close.
	select {
	case <-wsDone:
	case <-sess.tunnelDone:
	}

	// Cleanup.
	sess.guacdConn.Close()
	h.tunnelOpener.CloseTunnel(sess.tunnelID)
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

// --- Helpers ---

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
