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
	"net/http"
	"time"

	"github.com/hobnob/hobnob/server/db"
)

// EventPayload is the JSON body sent to webhook endpoints.
type EventPayload struct {
	Event     string      `json:"event"`
	Timestamp time.Time   `json:"timestamp"`
	Node      *NodeInfo   `json:"node,omitempty"`
	Run       *RunInfo    `json:"run,omitempty"`
	Details   any         `json:"details,omitempty"`
}

// NodeInfo contains node metadata included in webhook payloads.
type NodeInfo struct {
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
}

// NewDispatcher creates a new webhook dispatcher.
func NewDispatcher(queries *db.Queries) *Dispatcher {
	return &Dispatcher{
		queries: queries,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		maxRetries:     3,
		retryBaseDelay: 5 * time.Second,
		sem:            make(chan struct{}, 20), // max 20 concurrent deliveries
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
// and count of affected nodes.
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
