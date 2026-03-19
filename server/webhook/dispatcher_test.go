// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
	"github.com/daniel-alexander4/vigo/server/db"
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
	d.skipSSRFCheck = true // test server binds to 127.0.0.1

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

	// Check X-Vigo-Event.
	if ev := receivedHeaders.Get("X-Vigo-Event"); ev != "run.success" {
		t.Errorf("X-Vigo-Event = %q, want run.success", ev)
	}

	// Check X-Vigo-Signature format and correctness.
	sig := receivedHeaders.Get("X-Vigo-Signature")
	if sig == "" {
		t.Fatal("X-Vigo-Signature header missing")
	}

	const prefix = "sha256="
	if len(sig) <= len(prefix) || sig[:len(prefix)] != prefix {
		t.Fatalf("X-Vigo-Signature should start with %q, got %q", prefix, sig)
	}

	expectedSig := sign(secret, receivedBody)
	if sig != prefix+expectedSig {
		t.Errorf("X-Vigo-Signature = %q, want %q", sig, prefix+expectedSig)
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
	d.skipSSRFCheck = true // test server binds to 127.0.0.1
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

func TestValidateWebhookURL_ValidHTTPS(t *testing.T) {
	if err := validateWebhookURL("https://hooks.example.com/webhook"); err != nil {
		t.Errorf("valid HTTPS URL rejected: %v", err)
	}
}

func TestValidateWebhookURL_ValidHTTP(t *testing.T) {
	if err := validateWebhookURL("http://hooks.example.com/webhook"); err != nil {
		t.Errorf("valid HTTP URL rejected: %v", err)
	}
}

func TestValidateWebhookURL_Localhost(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1",
		"http://127.0.0.1:8080",
		"http://localhost",
		"http://localhost:3000",
	} {
		if err := validateWebhookURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}

func TestValidateWebhookURL_LoopbackIPv6(t *testing.T) {
	if err := validateWebhookURL("http://[::1]"); err == nil {
		t.Error("expected [::1] to be rejected")
	}
	if err := validateWebhookURL("http://[::1]:8080"); err == nil {
		t.Error("expected [::1]:8080 to be rejected")
	}
}

func TestValidateWebhookURL_PrivateIP(t *testing.T) {
	for _, u := range []string{
		"http://10.0.0.1",
		"http://192.168.1.1",
		"http://172.16.0.1",
		"http://10.255.255.255:9090",
	} {
		if err := validateWebhookURL(u); err == nil {
			t.Errorf("expected private IP %q to be rejected", u)
		}
	}
}

func TestValidateWebhookURL_LinkLocal(t *testing.T) {
	if err := validateWebhookURL("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Error("expected link-local 169.254.169.254 to be rejected")
	}
}

func TestValidateWebhookURL_NonHTTPScheme(t *testing.T) {
	for _, u := range []string{
		"ftp://example.com",
		"file:///etc/passwd",
		"gopher://example.com",
	} {
		if err := validateWebhookURL(u); err == nil {
			t.Errorf("expected non-HTTP scheme %q to be rejected", u)
		}
	}
}

func TestValidateWebhookURL_Empty(t *testing.T) {
	if err := validateWebhookURL(""); err == nil {
		t.Error("expected empty URL to be rejected")
	}
}

func TestDispatchRotation(t *testing.T) {
	var mu sync.Mutex
	var receivedBody []byte

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// We need a mock queries that returns webhooks for secret.rotated event.
	// Since Dispatch calls d.queries.ListActiveWebhooksByEvent which requires a real DB,
	// we'll test via deliver() directly with a pre-built payload.
	d := NewDispatcher(nil, DispatcherConfig{})
	d.httpClient = ts.Client()
	d.skipSSRFCheck = true

	wh := db.Webhook{
		ID:     uuid.New().String(),
		Url:    ts.URL,
		Secret: "rotation-secret",
		Events: `["secret.rotated"]`,
		Active: 1,
	}

	payload := EventPayload{
		Event:     "secret.rotated",
		Timestamp: time.Now().UTC(),
		Details: map[string]any{
			"secret_path":    "vigo/db/dsn",
			"affected_nodes": 5,
		},
	}
	body, _ := json.Marshal(payload)
	d.deliver(context.Background(), wh, "secret.rotated", body)

	mu.Lock()
	defer mu.Unlock()

	if receivedBody == nil {
		t.Fatal("no request received")
	}

	var got EventPayload
	if err := json.Unmarshal(receivedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Event != "secret.rotated" {
		t.Errorf("event = %q, want secret.rotated", got.Event)
	}

	details, ok := got.Details.(map[string]any)
	if !ok {
		t.Fatal("expected details to be a map")
	}
	if details["secret_path"] != "vigo/db/dsn" {
		t.Errorf("secret_path = %v, want vigo/db/dsn", details["secret_path"])
	}
}

func TestDeliverContextCancellation(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError) // always fail to trigger retries
	}))
	defer ts.Close()

	d := NewDispatcher(nil, DispatcherConfig{})
	d.httpClient = ts.Client()
	d.maxRetries = 5
	d.retryBaseDelay = 50 * time.Millisecond
	d.skipSSRFCheck = true

	wh := db.Webhook{
		ID:     uuid.New().String(),
		Url:    ts.URL,
		Secret: "cancel-secret",
		Events: `["test"]`,
		Active: 1,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay (should cut off retries).
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	d.deliver(ctx, wh, "test", []byte(`{}`))

	mu.Lock()
	defer mu.Unlock()

	// Should have fewer attempts than maxRetries+1 (6) since context was cancelled.
	if attempts >= 5 {
		t.Errorf("expected cancellation to stop retries early, got %d attempts", attempts)
	}
}

func TestDeliverRetryExhaustion(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable) // always fail
	}))
	defer ts.Close()

	d := NewDispatcher(nil, DispatcherConfig{})
	d.httpClient = ts.Client()
	d.maxRetries = 2
	d.retryBaseDelay = 1 * time.Millisecond
	d.skipSSRFCheck = true

	wh := db.Webhook{
		ID:     uuid.New().String(),
		Url:    ts.URL,
		Secret: "exhaust-secret",
		Events: `["test"]`,
		Active: 1,
	}

	d.deliver(context.Background(), wh, "test", []byte(`{}`))

	mu.Lock()
	defer mu.Unlock()

	// Should attempt initial + maxRetries = 3.
	if attempts != 3 {
		t.Errorf("expected 3 attempts (1 initial + 2 retries), got %d", attempts)
	}
}

func TestFormatRunEvent(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"success", "run.success"},
		{"failure", "run.failure"},
		{"partial", "run.partial"},
		{"", "run."},
	}
	for _, tc := range cases {
		got := FormatRunEvent(tc.status)
		if got != tc.want {
			t.Errorf("FormatRunEvent(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}
