// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/config"
)

// --- Sumo Logic ---

type sumoLogicTarget struct {
	cfg    *config.SumoLogicIntegration
	client *http.Client
}

func (s *sumoLogicTarget) Name() string                  { return "sumologic" }
func (s *sumoLogicTarget) Accepts(event Event) bool { return acceptsEvent(s.cfg.Events, event.Name) }
func (s *sumoLogicTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"timestamp": e.Timestamp.Format(time.RFC3339),
		"event":     e.Name,
		"hostname":  e.Hostname,
		"envoy_id":  e.EnvoyID,
		"summary":   e.Summary,
		"details":   e.Details,
		"source":    "vigo",
	}
	headers := map[string]string{
		"X-Sumo-Name": "vigo",
	}
	return postJSON(ctx, s.client, s.cfg.URL, headers, payload)
}

// --- Microsoft Sentinel (Log Analytics) ---

type sentinelTarget struct {
	cfg       *config.SentinelIntegration
	client    *http.Client
	sharedKey []byte // base64-decoded once at construction
}

// newSentinelTarget validates and decodes the Sentinel shared key once at
// startup. Decoding inline on every Dispatch (pre-0.66.64) silently
// swallowed base64 errors with `_, _ := …` and continued to HMAC against
// nil/garbage bytes — every signature was wrong, Sentinel returned 401,
// and the operator had no diagnostic. Validate-once-at-construction
// surfaces the misconfig at server boot via slog.Error and skips
// registering the target (other integrations stay unaffected).
func newSentinelTarget(cfg *config.SentinelIntegration, client *http.Client) (*sentinelTarget, error) {
	if cfg.SharedKey == "" {
		return nil, fmt.Errorf("shared_key is required")
	}
	// base64.DecodeString("") returns (nil, nil) — that's why the empty
	// check above is separate. We want a non-empty decoded key.
	keyBytes, err := base64.StdEncoding.DecodeString(cfg.SharedKey)
	if err != nil {
		return nil, fmt.Errorf("shared_key is not valid base64: %w", err)
	}
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("shared_key decoded to zero bytes")
	}
	return &sentinelTarget{cfg: cfg, client: client, sharedKey: keyBytes}, nil
}

func (s *sentinelTarget) Name() string                  { return "sentinel" }
func (s *sentinelTarget) Accepts(event Event) bool { return acceptsEvent(s.cfg.Events, event.Name) }
func (s *sentinelTarget) Dispatch(ctx context.Context, e Event) error {
	logType := s.cfg.LogType
	if logType == "" {
		logType = "VigoEvents"
	}

	payload := []map[string]any{{
		"TimeGenerated": e.Timestamp.Format(time.RFC3339),
		"Event":         e.Name,
		"Hostname":      e.Hostname,
		"EnvoyID":       e.EnvoyID,
		"Summary":       e.Summary,
		"Source":         "vigo",
	}}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	dateStr := time.Now().UTC().Format(time.RFC1123)
	sig := sentinelSignature(len(body), dateStr, s.sharedKey, s.cfg.WorkspaceID)
	url := fmt.Sprintf("https://%s.ods.opinsights.azure.com/api/logs?api-version=2016-04-01", s.cfg.WorkspaceID)

	headers := map[string]string{
		"Authorization":        sig,
		"Log-Type":             logType,
		"x-ms-date":           dateStr,
		"time-generated-field": "TimeGenerated",
	}
	return postJSON(ctx, s.client, url, headers, payload)
}

// sentinelSignature builds the Authorization header per Microsoft's
// HTTP Data Collector API spec. sharedKey is the pre-decoded HMAC key
// (validated in newSentinelTarget at construction).
func sentinelSignature(contentLength int, date string, sharedKey []byte, workspaceID string) string {
	stringToSign := fmt.Sprintf("POST\n%d\napplication/json\nx-ms-date:%s\n/api/logs", contentLength, date)
	mac := hmac.New(sha256.New, sharedKey)
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("SharedKey %s:%s", workspaceID, sig)
}
