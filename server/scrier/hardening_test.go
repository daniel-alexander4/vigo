// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package scrier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/httpsec"
)

// TestSetupSession_FailedSessionLeavesMap is the regression for the
// session-map leak: a setup that fails before the tunnel opens (here, no
// stored SSH key) must still be removed once the expiry window passes,
// after the poller has been able to read the error.
func TestSetupSession_FailedSessionLeavesMap(t *testing.T) {
	h, _ := newRelayTestHandler(t)
	h.sessionExpiry = 30 * time.Millisecond
	sess := &scrierSession{id: "fail-sess", envoyID: "e", hostname: "h", protocol: "ssh", user: "alice", status: statusWaiting}
	h.sessionsMu.Lock()
	h.sessions[sess.id] = sess
	h.sessionsMu.Unlock()

	h.setupSession(sess, &vigoauth.User{ID: "u-1", Username: "alice", Role: "admin"})

	if st, _ := sess.getStatus(); st != statusError {
		t.Fatalf("setup status = %s, want error", st)
	}
	if h.getSession(sess.id) == nil {
		t.Fatal("failed session must stay readable for the expiry window")
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.getSession(sess.id) != nil {
		if time.Now().After(deadline) {
			t.Fatal("failed session never left h.sessions")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestHandleRelay_RejectsForeignOrigin: a browser upgrade from another
// origin (cross-site WebSocket hijacking) is refused.
func TestHandleRelay_RejectsForeignOrigin(t *testing.T) {
	h, _ := newRelayTestHandler(t)
	sess, _, _ := newRelayTestSession(t, h)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/scrier/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := vigoauth.ContextWithUser(r.Context(), &vigoauth.User{Username: "alice", Role: "admin"})
		h.HandleRelay(w, r.WithContext(ctx))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/scrier/" + sess.id

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"https://evil.example"}},
	})
	if err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("a foreign-origin upgrade must be refused")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: want 403, got %v", resp)
	}
}

// TestSessionTemplatesHaveNoInlineCode pins the CSP contract of the
// session pages: every script and style is an external asset.
func TestSessionTemplatesHaveNoInlineCode(t *testing.T) {
	inlineScript := regexp.MustCompile(`<script>`)
	handler := regexp.MustCompile(`\son[a-z]+\s*=`)
	asset := regexp.MustCompile(`/scrier/assets/([a-z0-9.-]+)\?`)
	for name, tmpl := range map[string]string{"ssh": scrierSessionSSHHTML, "rdp": scrierSessionRDPHTML} {
		if inlineScript.MatchString(tmpl) {
			t.Errorf("%s template has an inline <script>", name)
		}
		if strings.Contains(tmpl, "<style") {
			t.Errorf("%s template has an inline <style>", name)
		}
		if strings.Contains(tmpl, "style=") {
			t.Errorf("%s template has a style= attribute", name)
		}
		if handler.MatchString(tmpl) {
			t.Errorf("%s template has an inline on*= handler", name)
		}
		for _, m := range asset.FindAllStringSubmatch(tmpl, -1) {
			if _, err := staticFS.Open("static/" + m[1]); err != nil {
				t.Errorf("%s template references missing asset %s", name, m[1])
			}
		}
	}
}

// TestHandleSession_SetsSecurityHeaders: both session pages carry a CSP
// and X-Frame-Options; the RDP page the locked one, the SSH page the
// terminal variant.
func TestHandleSession_SetsSecurityHeaders(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "e1", Hostname: "web1"}, nil)
	h := NewHandler(&fakeTunnelOpener{}, fi, nil, "test", time.Minute, time.Minute)
	for proto, want := range map[string]string{"ssh": httpsec.ScrierTerminalCSP, "rdp": httpsec.CSP} {
		req := httptest.NewRequest("GET", "/scrier/e1?protocol="+proto, nil)
		req.SetPathValue("id", "e1")
		req = req.WithContext(vigoauth.ContextWithUser(req.Context(), &vigoauth.User{Username: "alice", Role: "admin"}))
		rec := httptest.NewRecorder()
		h.HandleSession(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", proto, rec.Code)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: CSP = %q, want %q", proto, got, want)
		}
		if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Errorf("%s: X-Frame-Options missing", proto)
		}
	}
}

// TestHandleAsset_ServesEmbeddedAssets: the external scripts the pages
// load are served, and a traversal name is not.
func TestHandleAsset_ServesEmbeddedAssets(t *testing.T) {
	h, _ := newRelayTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, name := range []string{"scrier.css", "scrier-common.js", "scrier-ssh.js", "scrier-rdp.js"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/scrier/assets/"+name, nil))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s: status %d, %d bytes", name, rec.Code, rec.Body.Len())
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/scrier/assets/..%2fwebsocket.go", nil))
	if rec.Code == http.StatusOK {
		t.Error("traversal name must not be served")
	}
}
