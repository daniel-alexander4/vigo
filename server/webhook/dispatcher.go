// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package webhook provides asynchronous event dispatch to registered webhook
// endpoints with HMAC-SHA256 payload signing and retry support.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/hobnob/hobnob/server/db"
)

// EventPayload is the JSON body sent to webhook endpoints.
type EventPayload struct {
	Event     string      `json:"event"`
	Timestamp time.Time   `json:"timestamp"`
	Thrall      *ThrallInfo   `json:"thrall,omitempty"`
	Run       *RunInfo    `json:"run,omitempty"`
	Details   any         `json:"details,omitempty"`
}

// ThrallInfo contains thrall metadata included in webhook payloads.
type ThrallInfo struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	IP       string `json:"ip,omitempty"`
}

// RunInfo contains run metadata included in webhook payloads.
type RunInfo struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Changed int32  `json:"changed"`
	Failed  int32  `json:"failed"`
}

// Dispatcher sends webhook events to registered endpoints.
type Dispatcher struct {
	queries        *db.Queries
	httpClient     *http.Client
	maxRetries     int
	retryBaseDelay time.Duration
	sem            chan struct{} // bounds concurrent deliveries
	skipSSRFCheck  bool          // test-only: bypass SSRF validation
}

// DispatcherConfig holds configurable parameters for the webhook dispatcher.
type DispatcherConfig struct {
	Timeout       time.Duration
	MaxRetries    int
	MaxConcurrent int
	RetryDelay    time.Duration
}

// NewDispatcher creates a new webhook dispatcher with the given config.
// Zero-value fields in cfg use sensible defaults.
func NewDispatcher(queries *db.Queries, cfg DispatcherConfig) *Dispatcher {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 20
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 5 * time.Second
	}
	return &Dispatcher{
		queries: queries,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		maxRetries:     cfg.MaxRetries,
		retryBaseDelay: cfg.RetryDelay,
		sem:            make(chan struct{}, cfg.MaxConcurrent),
	}
}

// Dispatch queries for active webhooks matching the event and fires deliveries
// asynchronously. It never blocks the caller.
func (d *Dispatcher) Dispatch(ctx context.Context, event string, payload EventPayload) {
	payload.Event = event
	payload.Timestamp = time.Now().UTC()

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("webhook: failed to marshal payload", "event", event, "error", err)
		return
	}

	webhooks, err := d.queries.ListActiveWebhooksByEvent(ctx, event)
	if err != nil {
		slog.Error("webhook: failed to list webhooks", "event", event, "error", err)
		return
	}

	for _, wh := range webhooks {
		wh := wh
		go func() {
			d.sem <- struct{}{}        // acquire
			defer func() { <-d.sem }() // release
			d.deliver(ctx, wh, event, body)
		}()
	}
}

// DispatchRotation fires a secret.rotated webhook event with the secret path
// and count of affected thralls.
func (d *Dispatcher) DispatchRotation(ctx context.Context, event string, secretPath string, affected int) {
	d.Dispatch(ctx, event, EventPayload{
		Details: map[string]any{
			"secret_path":    secretPath,
			"affected_nodes": affected,
		},
	})
}

// deliver sends the payload to a single webhook endpoint with retries.
func (d *Dispatcher) deliver(ctx context.Context, wh db.Webhook, event string, body []byte) {
	signature := "sha256=" + sign(wh.Secret, body)

	if !d.skipSSRFCheck {
		if err := validateWebhookURL(wh.Url); err != nil {
			slog.Error("webhook: URL rejected by SSRF policy",
				"webhook_id", wh.ID, "url", wh.Url, "error", err)
			return
		}
	}

	for attempt := 0; attempt <= d.maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * d.retryBaseDelay
			select {
			case <-ctx.Done():
				slog.Warn("webhook: delivery cancelled during retry backoff",
					"webhook_id", wh.ID, "event", event)
				return
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.Url, bytes.NewReader(body))
		if err != nil {
			slog.Error("webhook: failed to create request",
				"webhook_id", wh.ID, "url", wh.Url, "error", err)
			return // bad URL, no point retrying
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hobnob-Signature", signature)
		req.Header.Set("X-Hobnob-Event", event)

		resp, err := d.httpClient.Do(req)
		if err != nil {
			slog.Warn("webhook: delivery failed",
				"webhook_id", wh.ID, "attempt", attempt+1, "error", err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode < 300 {
			slog.Info("webhook: delivered",
				"webhook_id", wh.ID, "event", event, "status", resp.StatusCode)
			return
		}

		slog.Warn("webhook: non-success response",
			"webhook_id", wh.ID, "attempt", attempt+1,
			"status", resp.StatusCode, "url", wh.Url)
	}

	slog.Error("webhook: delivery failed after retries",
		"webhook_id", wh.ID, "event", event,
		"url", wh.Url, "max_retries", d.maxRetries)
}

// sign computes the HMAC-SHA256 hex digest for the given secret and payload.
func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// validateWebhookURL rejects URLs that target internal networks (SSRF protection).
func validateWebhookURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("empty webhook URL")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q: only http and https are allowed", u.Scheme)
	}

	hostname := u.Hostname()
	if hostname == "" {
		return fmt.Errorf("empty hostname")
	}

	// Reject well-known localhost names.
	if hostname == "localhost" || hostname == "localhost.localdomain" {
		return fmt.Errorf("loopback address %q is not allowed", hostname)
	}

	ip := net.ParseIP(hostname)
	if ip == nil {
		// Not an IP literal — allow DNS names (resolution happens at delivery time).
		return nil
	}

	if ip.IsLoopback() {
		return fmt.Errorf("loopback address %s is not allowed", ip)
	}
	if ip.IsPrivate() {
		return fmt.Errorf("private network address %s is not allowed", ip)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("link-local address %s is not allowed", ip)
	}

	return nil
}

// FormatRunEvent returns the appropriate event name for a run status.
func FormatRunEvent(status string) string {
	switch status {
	case "success":
		return "run.success"
	case "failure":
		return "run.failure"
	default:
		return fmt.Sprintf("run.%s", status)
	}
}
