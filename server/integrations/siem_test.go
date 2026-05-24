// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package integrations

import (
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/config"
)

// TestNewSentinelTarget_EmptySharedKey pins the validation gate: a missing
// `shared_key` is rejected at construction so the operator sees an error
// at startup rather than every Sentinel dispatch silently HMAC-ing with a
// zero-byte key and getting 401-rejected by Microsoft Log Analytics.
func TestNewSentinelTarget_EmptySharedKey(t *testing.T) {
	_, err := newSentinelTarget(&config.SentinelIntegration{
		WorkspaceID: "ws-uuid",
	}, nil)
	if err == nil {
		t.Fatal("expected error for empty shared_key, got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected 'required' in error, got: %v", err)
	}
}

// TestNewSentinelTarget_InvalidBase64 pins the misconfig that the pre-0.66.64
// code silently swallowed via `_, _ := base64.DecodeString(...)`. A bogus
// shared_key now surfaces with a wrapped decode error at construction.
func TestNewSentinelTarget_InvalidBase64(t *testing.T) {
	_, err := newSentinelTarget(&config.SentinelIntegration{
		WorkspaceID: "ws-uuid",
		SharedKey:   "not!valid@base64!!",
	}, nil)
	if err == nil {
		t.Fatal("expected error for non-base64 shared_key, got nil")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("expected 'base64' in error, got: %v", err)
	}
}

// TestNewSentinelTarget_ValidBase64 confirms the happy path: a valid
// base64-encoded key decodes into the expected bytes and is cached on the
// target so per-dispatch decoding is eliminated.
func TestNewSentinelTarget_ValidBase64(t *testing.T) {
	// "dGVzdGtleQ==" is base64 of "testkey"
	target, err := newSentinelTarget(&config.SentinelIntegration{
		WorkspaceID: "ws-uuid",
		SharedKey:   "dGVzdGtleQ==",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(target.sharedKey) != "testkey" {
		t.Errorf("expected decoded sharedKey to be %q, got %q", "testkey", string(target.sharedKey))
	}
}

// TestSentinelSignature_KnownVector pins the HMAC output byte-for-byte
// against a hand-computed fixture. If this fixture drifts, the on-wire
// Authorization header has changed — operators with existing Sentinel
// workspace integrations would silently break.
func TestSentinelSignature_KnownVector(t *testing.T) {
	// Fixture computed via:
	//   python3 -c 'import hmac, hashlib, base64; \
	//     k=b"testkey"; \
	//     m=b"POST\n100\napplication/json\nx-ms-date:Sat, 23 May 2026 14:00:00 GMT\n/api/logs"; \
	//     print(base64.b64encode(hmac.new(k, m, hashlib.sha256).digest()).decode())'
	const wantHMAC = "howTon/4AH09YD7aGLTL0a9ivAFMYImvv7hYnrxAqH0="
	got := sentinelSignature(100, "Sat, 23 May 2026 14:00:00 GMT", []byte("testkey"), "ws-uuid")
	want := "SharedKey ws-uuid:" + wantHMAC
	if got != want {
		t.Errorf("signature mismatch:\n  want %q\n  got  %q", want, got)
	}
}
