// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//go:build unix

// Interactive shell on an enrolled envoy via the existing Scrier tunnel.
//
// Data flow is identical to the browser Scrier SSH path — the only
// difference is that this CLI command is the WebSocket client instead of
// xterm.js in a browser:
//
//   vigocli
//     │  POST /api/v1/scrier/connect           (creates scrier_session row)
//     │  GET  /api/v1/scrier/sessions/{id}/status  (poll until "ready")
//     │  WS   /ws/scrier/{id}                  (binary frames = raw PTY
//     │                                         bytes both directions;
//     │                                         text frames = JSON control
//     │                                         messages, currently only
//     │                                         `{"type":"resize",…}`)
//     ▼
//   Vigo server — runs the SSH client against 127.0.0.1:22 on the envoy
//                 via the agent's existing TunnelStream; no inbound port
//                 needs to be open on the customer side.
//
// The OS user the session logs in as is whichever regular user on the envoy
// holds the operator's SSH pubkey in their authorized_keys. The CLI
// auto-detects the operator's pubkey via the discovery chain in
// resolveOperatorPubkey (SSH_USER_AUTH → /proc/self/loginuid → SUDO_USER),
// sends it in the connect request, and the server scans + lands there.

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"nhooyr.io/websocket"
)

var scrierSSHCmd = &cobra.Command{
	Use:   "ssh <envoy>",
	Short: "Open an interactive shell on an envoy via the Scrier tunnel",
	Long: `Open an interactive shell on an enrolled envoy, routed through
the Vigo server and the envoy's agent-initiated gRPC stream. The envoy
never needs an inbound port open — the same tunnel that serves the
browser-based Scrier UI is reused.

The OS user the session lands in is whichever regular user on the envoy
has your SSH pubkey in their authorized_keys. The CLI auto-detects your
pubkey (sshd's $SSH_USER_AUTH if ExposeAuthInfo is on, else the audit
loginuid's home directory, else $SUDO_USER's) and sends it in the
connect request; pass --ssh-key-file to override the auto-detected key.

<envoy> may be either a hostname or an envoy UUID.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		timeout, _ := cmd.Flags().GetDuration("timeout")
		sshKeyFile, _ := cmd.Flags().GetString("ssh-key-file")
		return runScrierSSH(cmd.Context(), args[0], timeout, sshKeyFile)
	},
}

func init() {
	scrierSSHCmd.Flags().Duration("timeout", 30*time.Second, "How long to wait for the envoy's agent to bring the session to \"ready\"")
	scrierSSHCmd.Flags().String("ssh-key-file", "", "Path to an SSH public key file to use as the verify source. Overrides the auto-detection chain (SSH_USER_AUTH → loginuid → SUDO_USER).")
	scrierCmd.AddCommand(scrierSSHCmd)
}

// resolveOperatorPubkey walks the operator-pubkey discovery chain. First hit
// wins; the chain is documented on scrierSSHCmd. Returns the normalized
// pubkey ("type base64 [comment]") on success.
func resolveOperatorPubkey(explicitPath string) (string, error) {
	if explicitPath != "" {
		data, err := os.ReadFile(explicitPath)
		if err != nil {
			return "", fmt.Errorf("reading --ssh-key-file %s: %w", explicitPath, err)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			return "", fmt.Errorf("%s is empty", explicitPath)
		}
		return key, nil
	}

	// $SSH_USER_AUTH — sshd writes "method <args>" lines for the auth
	// methods used by THIS session. For pubkey auth: "publickey <type>
	// <base64>". Requires ExposeAuthInfo=yes in sshd_config.
	if path := os.Getenv("SSH_USER_AUTH"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if rest, ok := strings.CutPrefix(line, "publickey "); ok {
					return rest, nil
				}
			}
		}
	}

	// /proc/self/loginuid — Linux audit subsystem records the original
	// login uid as a process attribute; survives every sudo/su/su-
	// transition. "-1" / 4294967295 means unset (kernel without audit).
	if data, err := os.ReadFile("/proc/self/loginuid"); err == nil {
		s := strings.TrimSpace(string(data))
		if s != "" && s != "-1" && s != "4294967295" {
			if u, err := user.LookupId(s); err == nil {
				if key, err := readDefaultSSHPubkey(u.HomeDir); err == nil {
					return key, nil
				}
			}
		}
	}

	// $SUDO_USER — set by sudo, cleared by `su -`. Belt-and-suspenders.
	if name := os.Getenv("SUDO_USER"); name != "" {
		if u, err := user.Lookup(name); err == nil {
			if key, err := readDefaultSSHPubkey(u.HomeDir); err == nil {
				return key, nil
			}
		}
	}

	return "", errors.New("cannot auto-detect your SSH pubkey " +
		"(no SSH_USER_AUTH, no usable /proc/self/loginuid, no SUDO_USER); " +
		"pass --ssh-key-file PATH, or set `ExposeAuthInfo yes` in /etc/ssh/sshd_config so sshd records the key you authenticated with")
}

// readDefaultSSHPubkey tries the standard pubkey names in `<home>/.ssh/`,
// in the same order cli/users.go's `webusers create --ssh-key-file` users
// will be familiar with.
func readDefaultSSHPubkey(home string) (string, error) {
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub"} {
		data, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err != nil {
			continue
		}
		key := strings.TrimSpace(string(data))
		if key != "" {
			return key, nil
		}
	}
	return "", fmt.Errorf("no SSH pubkey at %s/.ssh/id_ed25519.pub or id_rsa.pub", home)
}

// runScrierSSH is the entry point extracted for testability — callers can
// pass a short timeout and a pre-resolved envoy arg in unit tests.
func runScrierSSH(ctx context.Context, envoyArg string, setupTimeout time.Duration, sshKeyFile string) error {
	// Refuse early on non-TTY so we don't silently mangle piped usage.
	stdinFd := int(os.Stdin.Fd())
	stdoutFd := int(os.Stdout.Fd())
	if !term.IsTerminal(stdinFd) || !term.IsTerminal(stdoutFd) {
		return fmt.Errorf("stdin/stdout must be a TTY (cannot pipe `vigocli scrier ssh`)")
	}

	pubkey, err := resolveOperatorPubkey(sshKeyFile)
	if err != nil {
		return err
	}

	client := newAPIClient()

	envoyID, err := resolveEnvoy(client, envoyArg)
	if err != nil {
		return fmt.Errorf("resolving envoy %q: %w", envoyArg, err)
	}

	// --- Phase 1: create the session. ---
	var connectResp struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if err := client.post("/api/v1/scrier/connect", map[string]any{
		"envoy_id":       envoyID,
		"protocol":       "ssh",
		"port":           22,
		"ssh_public_key": pubkey,
		// width/height are for RDP/VNC canvas sizing; SSH uses the
		// first post-connect resize to set pty dimensions. Server
		// defaults these to 1024x768 when zero.
		"width":  0,
		"height": 0,
	}, &connectResp); err != nil {
		return fmt.Errorf("creating scrier session: %w", err)
	}
	if connectResp.SessionID == "" {
		return fmt.Errorf("server did not return a session_id")
	}

	// --- Phase 1.5: poll until ready or error. ---
	if err := waitForScrierReady(ctx, client, connectResp.SessionID, setupTimeout); err != nil {
		return err
	}

	// --- Phase 2: upgrade to WebSocket and relay. ---
	wsURL, err := scrierWebSocketURL(serverURL, connectResp.SessionID)
	if err != nil {
		return fmt.Errorf("building ws url: %w", err)
	}

	// Reuse the REST client's TLS posture (InsecureSkipVerify on
	// localhost, system pool elsewhere) so the same trust decisions
	// apply to REST and WS.
	wsHTTP := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: isLocalhost(serverURL)},
		},
	}
	hdr := http.Header{}
	if token := loadAuthToken(); token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}

	wsConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: wsHTTP,
		HTTPHeader: hdr,
	})
	if err != nil {
		return fmt.Errorf("opening scrier websocket: %w", err)
	}
	// 64 KiB matches the server's read limit (websocket.go:745).
	wsConn.SetReadLimit(64 * 1024)
	defer wsConn.Close(websocket.StatusNormalClosure, "cli exit")

	// --- Terminal raw mode. ---
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("putting terminal in raw mode: %w", err)
	}
	// Any exit path from here must restore the terminal before returning
	// output to the operator's normal shell.
	restored := false
	restore := func() {
		if !restored {
			_ = term.Restore(stdinFd, oldState)
			restored = true
		}
	}
	defer restore()

	// Send the initial resize so the remote pty matches the local
	// window before the first keystroke is interpreted.
	if err := sendResize(ctx, wsConn, stdoutFd); err != nil {
		return fmt.Errorf("sending initial resize: %w", err)
	}

	// --- SIGWINCH → resize relay. ---
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)

	// Track which side closed so the caller can distinguish "remote
	// shell exited cleanly" from "connection blew up".
	type endCause struct {
		err    error
		remote bool
	}
	endCh := make(chan endCause, 2)

	// --- WS → stdout (binary = pty bytes; text ignored). ---
	go func() {
		for {
			mt, data, readErr := wsConn.Read(ctx)
			if readErr != nil {
				endCh <- endCause{err: readErr, remote: true}
				return
			}
			if mt == websocket.MessageBinary {
				if _, werr := os.Stdout.Write(data); werr != nil {
					endCh <- endCause{err: werr, remote: false}
					return
				}
			}
			// Text frames (JSON control messages) are server→client
			// only for future use; ignore in V1.
		}
	}()

	// --- stdin → WS. ---
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				if werr := wsConn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					endCh <- endCause{err: werr, remote: false}
					return
				}
			}
			if readErr != nil {
				endCh <- endCause{err: readErr, remote: false}
				return
			}
		}
	}()

	// --- Resize loop, exits when winchCh closes via signal.Stop. ---
	go func() {
		for range winchCh {
			_ = sendResize(ctx, wsConn, stdoutFd)
		}
	}()

	// Block until one side reports an end cause.
	cause := <-endCh
	restore()

	// Classify the end. nhooyr's Read returns a CloseError on a clean
	// WS close; StatusNormalClosure + StatusGoingAway are normal ends
	// (user typed `exit`, server shut down). Anything else is abnormal.
	var closeErr websocket.CloseError
	if errors.As(cause.err, &closeErr) {
		switch closeErr.Code {
		case websocket.StatusNormalClosure, websocket.StatusGoingAway:
			return nil
		}
	}
	// Also treat stdin EOF (user hit Ctrl-D which the remote shell
	// turned into a clean exit) as a normal end — but we only know the
	// local side ended; we still close the WS and exit 0.
	if !cause.remote && errors.Is(cause.err, io.EOF) {
		return nil
	}
	if cause.err != nil {
		fmt.Fprintf(os.Stderr, "\r\nsession ended: %v\r\n", cause.err)
		return cause.err
	}
	return nil
}

// waitForScrierReady polls the session status endpoint until it reports
// "ready", "error", or the timeout expires. Returns nil only on "ready".
func waitForScrierReady(ctx context.Context, client *apiClient, sessionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	for {
		var st struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		path := "/api/v1/scrier/sessions/" + sessionID + "/status"
		if err := client.get(path, &st); err != nil {
			return fmt.Errorf("polling scrier status: %w", err)
		}
		switch st.Status {
		case "ready":
			return nil
		case "error":
			if st.Error != "" {
				return fmt.Errorf("scrier setup failed: %s", st.Error)
			}
			return fmt.Errorf("scrier setup failed (no detail)")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("scrier session did not reach ready within %s (last status: %q)", timeout, st.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// sendResize reads the current terminal size and pushes it to the server
// as the exact JSON shape relaySSH expects (websocket.go:799-805).
func sendResize(ctx context.Context, wsConn *websocket.Conn, fd int) error {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return err
	}
	msg, _ := json.Marshal(struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}{Type: "resize", Cols: cols, Rows: rows})
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return wsConn.Write(wctx, websocket.MessageText, msg)
}

// scrierWebSocketURL converts the CLI's --server base URL into the
// ws(s):// URL for the relay endpoint.
func scrierWebSocketURL(base, sessionID string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported server scheme %q (want http or https)", u.Scheme)
	}
	u.Path = "/ws/scrier/" + sessionID
	u.RawQuery = ""
	return u.String(), nil
}

