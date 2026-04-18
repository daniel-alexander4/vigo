// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//go:build unix

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
