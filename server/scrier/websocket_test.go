// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package scrier

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	vigoauth "git.alexander4.io/vigo/server/auth"
)

// fakeTunnelOpener stands in for the gRPC-backed TunnelOpener. The relay
// tests don't open real tunnels — they wire pre-built io.Pipes into the
// session directly and only need CloseTunnel to be called exactly once
// per session (asserted via the counter).
type fakeTunnelOpener struct {
	closeCount atomic.Int32
}

func (f *fakeTunnelOpener) OpenTunnel(_, _, _ string, _ uint32, _ *SSHPubkeyAuth) (string, string, chan struct{}, error) {
	return "tunnel-id", "127.0.0.1:0", make(chan struct{}), nil
}

func (f *fakeTunnelOpener) CloseTunnel(_ string) {
	f.closeCount.Add(1)
}

func (f *fakeTunnelOpener) SSHResult(_ string) (string, string) {
	return "operator", ""
}

func (f *fakeTunnelOpener) ActiveSessions() int  { return 0 }
func (f *fakeTunnelOpener) HasAgentStream(string) bool { return true }
func (f *fakeTunnelOpener) GuacdAddr() string    { return "127.0.0.1:0" }

// newRelayTestSession constructs a relay-ready scrierSession backed by
// in-memory pipes. The ptyOut/ptyIn pair lets tests inject SSH-side
// bytes via writes to ptyOutWriter and read what relaySSH sent into the
// PTY via ptyInReader. Tunnel close fires through fakeTunnelOpener.
func newRelayTestSession(t *testing.T, h *Handler) (sess *scrierSession, ptyOutWriter *io.PipeWriter, ptyInReader *io.PipeReader) {
	t.Helper()
	ptyOutReader, ptyOutWriter := io.Pipe()
	ptyInReader, ptyInWriter := io.Pipe()
	t.Cleanup(func() {
		_ = ptyOutReader.Close()
		_ = ptyOutWriter.Close()
		_ = ptyInReader.Close()
		_ = ptyInWriter.Close()
	})

	sess = &scrierSession{
		id:         "sess-test",
		envoyID:    "envoy-test",
		hostname:   "test.example.com",
		protocol:   "ssh",
		user:       "alice",
		status:     statusReady,
		createdAt:  time.Now(),
		tunnelID:   "tunnel-id",
		tunnelDone: make(chan struct{}),
		ptyOut:     ptyOutReader,
		ptyIn:      ptyInWriter,
	}
	h.sessionsMu.Lock()
	h.sessions[sess.id] = sess
	h.sessionsMu.Unlock()
	return sess, ptyOutWriter, ptyInReader
}

// newRelayTestHandler builds a Handler with no DB, no audit writer, and
// just enough wiring for HandleRelay to work end-to-end. The fake
// tunnel opener is returned so tests can assert on close count.
func newRelayTestHandler(t *testing.T) (*Handler, *fakeTunnelOpener) {
	t.Helper()
	opener := &fakeTunnelOpener{}
	h := NewHandler(opener, nil, nil, "test", 5*time.Minute, 12*time.Minute)
	return h, opener
}

// dialRelay opens a WebSocket to the relay endpoint with an admin
// context attached. Returns the live connection; the caller closes.
func dialRelay(t *testing.T, h *Handler, sessionID, username string) (*websocket.Conn, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/scrier/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := vigoauth.ContextWithUser(r.Context(), &vigoauth.User{Username: username, Role: "admin"})
		h.HandleRelay(w, r.WithContext(ctx))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/scrier/" + sessionID
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	return conn, srv
}

// --- claim() race safety ---

func TestSessionClaim_OnlyOneCallerWins(t *testing.T) {
	const goroutines = 32
	sess := &scrierSession{}
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if sess.claim() {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Errorf("expected exactly 1 claim winner, got %d", got)
	}
}

// --- HandleRelay rejects an already-claimed session ---

func TestHandleRelay_RejectsClaimedSession(t *testing.T) {
	h, _ := newRelayTestHandler(t)
	sess, _, _ := newRelayTestSession(t, h)
	if !sess.claim() {
		t.Fatal("first claim should win")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/scrier/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := vigoauth.ContextWithUser(r.Context(), &vigoauth.User{Username: sess.user, Role: "admin"})
		h.HandleRelay(w, r.WithContext(ctx))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/ws/scrier/" + sess.id)
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 on claimed session, got %d", resp.StatusCode)
	}
}

// --- Relay close paths (characterization) ---

func TestRelaySSH_HappyPathClose(t *testing.T) {
	h, opener := newRelayTestHandler(t)
	sess, ptyOut, ptyIn := newRelayTestSession(t, h)

	conn, _ := dialRelay(t, h, sess.id, sess.user)

	// Round-trip: server-side PTY sends bytes; client receives them.
	go func() { _, _ = ptyOut.Write([]byte("hello\n")) }()
	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	typ, got, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if typ != websocket.MessageBinary || string(got) != "hello\n" {
		t.Errorf("expected binary 'hello\\n', got type=%v data=%q", typ, got)
	}

	// Client-to-server: write and confirm it lands in the PTY.
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := conn.Write(writeCtx, websocket.MessageBinary, []byte("ack")); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	writeCancel()
	buf := make([]byte, 3)
	if _, err := io.ReadFull(ptyIn, buf); err != nil {
		t.Fatalf("ptyIn read: %v", err)
	}
	if string(buf) != "ack" {
		t.Errorf("expected ack in PTY, got %q", buf)
	}

	// Server-side close: PTY EOF triggers relaySSH return → HandleRelay
	// cleanup → tunnel CloseTunnel exactly once.
	_ = ptyOut.Close()
	waitForCleanup(t, opener, 1)
}

func TestRelaySSH_RemoteClose(t *testing.T) {
	h, opener := newRelayTestHandler(t)
	sess, ptyOut, _ := newRelayTestSession(t, h)

	_, _ = dialRelay(t, h, sess.id, sess.user)

	// Remote-side close: ptyOut errors mid-stream (simulates ssh process
	// dying on the envoy). The WS-side reader should wake within ms via
	// the deferred cancel(), not wait minutes for TCP timeout.
	_ = ptyOut.CloseWithError(io.ErrUnexpectedEOF)
	waitForCleanup(t, opener, 1)
}

func TestRelaySSH_LocalClose(t *testing.T) {
	h, opener := newRelayTestHandler(t)
	sess, _, _ := newRelayTestSession(t, h)

	conn, _ := dialRelay(t, h, sess.id, sess.user)

	// Client closes the WebSocket: relay's WS-reader errors, cancel()
	// fires, PTY-reader stays blocked until cleanupSession closes its
	// source (handled by HandleRelay's final cleanupSession call).
	_ = conn.Close(websocket.StatusNormalClosure, "client done")
	waitForCleanup(t, opener, 1)
}

// --- Session-expiry race against relay claim ---

func TestSessionExpiry_ClaimRaceWithRelay(t *testing.T) {
	h, opener := newRelayTestHandler(t)
	h.sessionExpiry = 50 * time.Millisecond

	sess := &scrierSession{
		id:         "race-sess",
		envoyID:    "envoy",
		hostname:   "h",
		protocol:   "ssh",
		user:       "alice",
		status:     statusReady,
		createdAt:  time.Now(),
		tunnelID:   "tunnel-id",
		tunnelDone: make(chan struct{}),
	}
	h.sessionsMu.Lock()
	h.sessions[sess.id] = sess
	h.sessionsMu.Unlock()

	// Arm the expiry timer manually with the injected duration. The
	// production path arms it inside setupSession; the test exercises
	// just the lifecycle, not the full setup chain.
	time.AfterFunc(h.sessionExpiry, func() {
		if !sess.claim() {
			return
		}
		h.cleanupSession(sess)
		h.removeSession(sess.id)
	})

	// Wait for expiry to claim + clean up.
	time.Sleep(150 * time.Millisecond)

	if got := opener.closeCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 tunnel close on expiry, got %d", got)
	}

	// Late relay attempt must see the session already removed (404).
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/scrier/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := vigoauth.ContextWithUser(r.Context(), &vigoauth.User{Username: sess.user, Role: "admin"})
		h.HandleRelay(w, r.WithContext(ctx))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/ws/scrier/" + sess.id)
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after expiry, got %d", resp.StatusCode)
	}
}

// waitForCleanup polls fakeTunnelOpener.closeCount up to 2s for the
// expected count. Avoids sleep-fixed-duration so tests run fast on
// healthy machines but stay reliable under CI load.
func waitForCleanup(t *testing.T, opener *fakeTunnelOpener, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := opener.closeCount.Load(); got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("expected CloseTunnel called %d times within 2s, got %d", want, opener.closeCount.Load())
}
