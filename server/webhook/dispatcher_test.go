// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hobnob/hobnob/server/db"
)

func TestHMACSigning(t *testing.T) {
	secret := "my-webhook-secret"
	payload := []byte(`{"event":"run.success","timestamp":"2025-01-01T00:00:00Z"}`)

	got := sign(secret, payload)

	// Compute expected HMAC-SHA256 independently.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Errorf("sign() = %q, want %q", got, want)
	}
}

func TestSignDeterministic(t *testing.T) {
	secret := "test-secret"
	payload := []byte("hello world")

	sig1 := sign(secret, payload)
	sig2 := sign(secret, payload)

	if sig1 != sig2 {
		t.Error("sign() should be deterministic for same inputs")
	}

	sig3 := sign("different-secret", payload)
	if sig1 == sig3 {
		t.Error("sign() should produce different output for different secrets")
	}
}

// mockWebhookQuerier implements the WebhookQuerier interface for testing.
type mockWebhookQuerier struct {
	webhooks []db.Webhook
}

func (m *mockWebhookQuerier) ListActiveWebhooksByEvent(_ interface{}, event string) ([]db.Webhook, error) {
	var result []db.Webhook
	for _, wh := range m.webhooks {
		var events []string
		json.Unmarshal([]byte(wh.Events), &events)
		for _, e := range events {
			if e == event {
				result = append(result, wh)
				break
			}
		}
	}
	return result, nil
}

func TestDispatchSendsCorrectHeaders(t *testing.T) {
	var mu sync.Mutex
	var receivedHeaders http.Header
	var receivedBody []byte

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedHeaders = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	secret := "test-secret-123"
	wh := db.Webhook{
		ID:     uuid.New().String(),
		Url:    ts.URL,
		Secret: secret,
		Events: `["run.success"]`,
		Active: 1,
	}

	d := NewDispatcher(nil, DispatcherConfig{})
	d.httpClient = ts.Client()

	// Deliver directly to test headers.
	payload := EventPayload{
		Event:     "run.success",
		Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	body, _ := json.Marshal(payload)

	d.deliver(context.Background(), wh, "run.success", body)

	mu.Lock()
	defer mu.Unlock()

	if receivedHeaders == nil {
		t.Fatal("no request received")
	}

	// Check Content-Type.
	if ct := receivedHeaders.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Check X-Hobnob-Event.
	if ev := receivedHeaders.Get("X-Hobnob-Event"); ev != "run.success" {
		t.Errorf("X-Hobnob-Event = %q, want run.success", ev)
	}

	// Check X-Hobnob-Signature format and correctness.
	sig := receivedHeaders.Get("X-Hobnob-Signature")
	if sig == "" {
		t.Fatal("X-Hobnob-Signature header missing")
	}

	const prefix = "sha256="
	if len(sig) <= len(prefix) || sig[:len(prefix)] != prefix {
		t.Fatalf("X-Hobnob-Signature should start with %q, got %q", prefix, sig)
	}

	expectedSig := sign(secret, receivedBody)
	if sig != prefix+expectedSig {
		t.Errorf("X-Hobnob-Signature = %q, want %q", sig, prefix+expectedSig)
	}
}

func TestDeliverRetriesOnServerError(t *testing.T) {
	var mu sync.Mutex
	var attempts int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		a := attempts
		mu.Unlock()
		if a <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	wh := db.Webhook{
		ID:     uuid.New().String(),
		Url:    ts.URL,
		Secret: "secret",
		Events: `["run.failure"]`,
		Active: 1,
	}

	d := NewDispatcher(nil, DispatcherConfig{})
	d.httpClient = ts.Client()
	d.maxRetries = 3
	d.retryBaseDelay = 1 * time.Millisecond // fast retries for test

	body := []byte(`{"event":"run.failure"}`)
	d.deliver(context.Background(), wh, "run.failure", body)

	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Errorf("expected 3 delivery attempts, got %d", attempts)
	}
}
