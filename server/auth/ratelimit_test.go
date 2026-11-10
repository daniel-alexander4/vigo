// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http/httptest"
	"testing"
)

// TestAuthLimiter_BurstThenDeny proves the per-key bucket allows up to `burst`
// immediate attempts and then throttles. A very low sustained rate keeps the
// bucket from refilling within the test so the deny is deterministic.
func TestAuthLimiter_BurstThenDeny(t *testing.T) {
	al := newAuthLimiter(1, 3) // 1/min sustained, burst 3

	for i := 0; i < 3; i++ {
		if !al.allow("1.2.3.4") {
			t.Fatalf("attempt %d within burst should be allowed", i+1)
		}
	}
	if al.allow("1.2.3.4") {
		t.Fatal("4th attempt past the burst should be throttled")
	}

	// A different key has its own independent bucket.
	if !al.allow("5.6.7.8") {
		t.Fatal("a fresh key must not be throttled by another key's usage")
	}
}

func TestClientIP(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4:5555":      "1.2.3.4",
		"[::1]:8443":        "::1",
		"no-port-remoteadd": "no-port-remoteadd",
	}
	for remoteAddr, want := range cases {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = remoteAddr
		if got := clientIP(r); got != want {
			t.Errorf("clientIP(%q) = %q, want %q", remoteAddr, got, want)
		}
	}
}
