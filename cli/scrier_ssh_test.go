// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScrierWebSocketURL(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		session string
		want    string
		wantErr bool
	}{
		{"https → wss", "https://vigo.example.com:8443", "abc-123", "wss://vigo.example.com:8443/ws/scrier/abc-123", false},
		{"http → ws", "http://vigo.example.com:8443", "abc-123", "ws://vigo.example.com:8443/ws/scrier/abc-123", false},
		{"localhost https", "https://localhost:8443", "sid", "wss://localhost:8443/ws/scrier/sid", false},
		{"strips existing path", "https://vigo.example.com:8443/foo?bar=1", "sid", "wss://vigo.example.com:8443/ws/scrier/sid", false},
		{"bad scheme", "ftp://nope", "sid", "", true},
		{"malformed url", "::::", "sid", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scrierWebSocketURL(tc.base, tc.session)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("scrierWebSocketURL(%q) = %q, want error", tc.base, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("scrierWebSocketURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

// TestWaitForScrierReady_TransitionsToReady verifies the poll loop
// advances through waiting → connecting → ready and returns nil.
func TestWaitForScrierReady_TransitionsToReady(t *testing.T) {
	var pollN atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/status") {
			http.NotFound(w, r)
			return
		}
		n := pollN.Add(1)
		var status string
		switch n {
		case 1:
			status = "waiting"
		case 2:
			status = "connecting"
		default:
			status = "ready"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	}))
	defer srv.Close()

	origURL := serverURL
	serverURL = srv.URL
	defer func() { serverURL = origURL }()

	client := newAPIClient()
	// The loop ticks every 1s and checks deadline after each poll —
	// 5s gives us three polls worth of headroom without being slow.
	if err := waitForScrierReady(context.Background(), client, "sess-1", 5*time.Second); err != nil {
		t.Fatalf("waitForScrierReady: %v", err)
	}
	if pollN.Load() < 3 {
		t.Errorf("expected at least 3 polls, got %d", pollN.Load())
	}
}

// TestWaitForScrierReady_ErrorStatus surfaces the server's error message.
func TestWaitForScrierReady_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "error",
			"error":  "agent never opened tunnel",
		})
	}))
	defer srv.Close()

	origURL := serverURL
	serverURL = srv.URL
	defer func() { serverURL = origURL }()

	err := waitForScrierReady(context.Background(), newAPIClient(), "sess-2", 5*time.Second)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "agent never opened tunnel") {
		t.Errorf("error did not include server-provided detail: %v", err)
	}
}

// TestResolveOperatorPubkey_ExplicitPath wins over every other source.
func TestResolveOperatorPubkey_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "explicit.pub")
	want := "ssh-ed25519 AAAA-explicit user@laptop\n"
	if err := os.WriteFile(keyPath, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	// Even with SSH_USER_AUTH set to a different key, the explicit path
	// must win — that's the operator-override contract.
	authPath := filepath.Join(dir, "auth.txt")
	_ = os.WriteFile(authPath, []byte("publickey ssh-ed25519 AAAA-fromauth\n"), 0o600)
	t.Setenv("SSH_USER_AUTH", authPath)

	got, err := resolveOperatorPubkey(keyPath)
	if err != nil {
		t.Fatalf("resolveOperatorPubkey: %v", err)
	}
	if got != strings.TrimSpace(want) {
		t.Errorf("got %q, want %q", got, strings.TrimSpace(want))
	}
}

// TestResolveOperatorPubkey_ExplicitPath_Empty rejects an empty file.
func TestResolveOperatorPubkey_ExplicitPath_Empty(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "empty.pub")
	if err := os.WriteFile(keyPath, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveOperatorPubkey(keyPath); err == nil {
		t.Fatalf("expected error on empty key file")
	}
}

// TestResolveOperatorPubkey_SSHUserAuth parses sshd's per-session auth-info
// file and returns the publickey line.
func TestResolveOperatorPubkey_SSHUserAuth(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.txt")
	// Real sshd writes one method per line; pubkey lines include the
	// rest verbatim. Extra method lines should be skipped.
	contents := "publickey ssh-ed25519 AAAA-the-real-key user@laptop\n"
	if err := os.WriteFile(authPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_USER_AUTH", authPath)

	// No --ssh-key-file override; should hit the SSH_USER_AUTH branch.
	got, err := resolveOperatorPubkey("")
	if err != nil {
		t.Fatalf("resolveOperatorPubkey: %v", err)
	}
	want := "ssh-ed25519 AAAA-the-real-key user@laptop"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestReadDefaultSSHPubkey_PrefersEd25519 confirms id_ed25519.pub wins over
// id_rsa.pub when both are present.
func TestReadDefaultSSHPubkey_PrefersEd25519(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ssh, "id_rsa.pub"), []byte("ssh-rsa AAAA-rsa rsa@x\n"), 0o600)
	_ = os.WriteFile(filepath.Join(ssh, "id_ed25519.pub"), []byte("ssh-ed25519 AAAA-ed ed@x\n"), 0o600)

	got, err := readDefaultSSHPubkey(home)
	if err != nil {
		t.Fatalf("readDefaultSSHPubkey: %v", err)
	}
	if !strings.HasPrefix(got, "ssh-ed25519 ") {
		t.Errorf("expected ed25519 to win, got %q", got)
	}
}

// TestReadDefaultSSHPubkey_FallsBackToRSA when only id_rsa.pub is present.
func TestReadDefaultSSHPubkey_FallsBackToRSA(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ssh, "id_rsa.pub"), []byte("ssh-rsa AAAA-rsa rsa@x\n"), 0o600)

	got, err := readDefaultSSHPubkey(home)
	if err != nil {
		t.Fatalf("readDefaultSSHPubkey: %v", err)
	}
	if !strings.HasPrefix(got, "ssh-rsa ") {
		t.Errorf("expected rsa fallback, got %q", got)
	}
}

// TestReadDefaultSSHPubkey_NoKeys returns an error when neither default key exists.
func TestReadDefaultSSHPubkey_NoKeys(t *testing.T) {
	home := t.TempDir()
	if _, err := readDefaultSSHPubkey(home); err == nil {
		t.Fatalf("expected error when no default pubkey is present")
	}
}

// TestResolveOperatorPubkey_AllSourcesEmpty surfaces the hint about
// --ssh-key-file when nothing in the chain hits.
func TestResolveOperatorPubkey_AllSourcesEmpty(t *testing.T) {
	t.Setenv("SSH_USER_AUTH", "")
	t.Setenv("SUDO_USER", "")
	// /proc/self/loginuid still exists on Linux test runners and may
	// return a valid uid pointing at the test runner's home — which
	// happens to NOT have ~/.ssh/id_*.pub in CI environments. The
	// chain falls through and we get the hint.
	_, err := resolveOperatorPubkey("")
	if err == nil {
		t.Skip("test runner has discoverable SSH keys; skipping")
	}
	if !strings.Contains(err.Error(), "--ssh-key-file") {
		t.Errorf("error must mention the --ssh-key-file fallback hint: %v", err)
	}
	// Each fallback branch should contribute its own verdict — generic
	// "no X, no Y, no Z" was the prior shape and misled operators.
	for _, want := range []string{"SSH_USER_AUTH:", "loginuid:", "SUDO_USER:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name the %s branch verdict; got: %v", want, err)
		}
	}
}

// TestResolveOperatorPubkey_SUDOUserSetButNoPubkey is the regression for
// the 2026-05-15 misleading error: SUDO_USER was set but the operator's
// home had no id_*.pub, and the chain reported "no SUDO_USER" — a lie.
// The verdict must now name the actual failure (key file missing), not
// claim the env var is unset.
func TestResolveOperatorPubkey_SUDOUserSetButNoPubkey(t *testing.T) {
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("`nobody` user not present on this system")
	}
	t.Setenv("SSH_USER_AUTH", "")
	t.Setenv("SUDO_USER", "nobody")
	_, err := resolveOperatorPubkey("")
	if err == nil {
		t.Skip("`nobody`'s home has discoverable SSH keys on this runner")
	}
	if strings.Contains(err.Error(), "SUDO_USER: not set") {
		t.Errorf("verdict must NOT claim SUDO_USER is unset when SUDO_USER=nobody; got: %v", err)
	}
	if !strings.Contains(err.Error(), "SUDO_USER: nobody") {
		t.Errorf("verdict must name SUDO_USER=nobody and the read failure; got: %v", err)
	}
}

// TestResolveOperatorPubkey_SUDOUserNotInPasswd covers the user.Lookup
// failure path: SUDO_USER set, but no matching passwd entry.
func TestResolveOperatorPubkey_SUDOUserNotInPasswd(t *testing.T) {
	t.Setenv("SSH_USER_AUTH", "")
	t.Setenv("SUDO_USER", "zzz-vigo-bogus-12345")
	_, err := resolveOperatorPubkey("")
	if err == nil {
		t.Fatal("expected error for bogus SUDO_USER, got nil")
	}
	if !strings.Contains(err.Error(), `SUDO_USER: "zzz-vigo-bogus-12345" not in passwd`) {
		t.Errorf("verdict must name the failing passwd lookup; got: %v", err)
	}
}

// TestResolveOperatorPubkey_SSHUserAuthNoPubkeyLine covers the SSH_USER_AUTH
// branch when sshd recorded auth methods but none were pubkey (e.g.,
// password-only auth). The verdict must name the missing publickey line.
func TestResolveOperatorPubkey_SSHUserAuthNoPubkeyLine(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.txt")
	if err := os.WriteFile(authPath, []byte("password\nkeyboard-interactive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_USER_AUTH", authPath)
	t.Setenv("SUDO_USER", "")
	_, err := resolveOperatorPubkey("")
	if err == nil {
		t.Skip("test runner had a usable loginuid or SUDO_USER fallback")
	}
	if !strings.Contains(err.Error(), "no `publickey` line") {
		t.Errorf("verdict must name the missing publickey line; got: %v", err)
	}
}

// TestWaitForScrierReady_Timeout exits with a useful message when the
// session never reaches ready before the deadline.
func TestWaitForScrierReady_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "waiting"})
	}))
	defer srv.Close()

	origURL := serverURL
	serverURL = srv.URL
	defer func() { serverURL = origURL }()

	err := waitForScrierReady(context.Background(), newAPIClient(), "sess-3", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "did not reach ready") {
		t.Errorf("error message should mention the stuck status: %v", err)
	}
}
