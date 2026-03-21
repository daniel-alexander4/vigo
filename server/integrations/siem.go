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

	"github.com/daniel-alexander4/vigo/server/config"
)

// --- Sumo Logic ---

type sumoLogicTarget struct {
	cfg    *config.SumoLogicIntegration
	client *http.Client
}

func (s *sumoLogicTarget) Name() string                  { return "sumologic" }
func (s *sumoLogicTarget) Accepts(eventName string) bool { return acceptsEvent(s.cfg.Events, eventName) }
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
	cfg    *config.SentinelIntegration
	client *http.Client
}

func (s *sentinelTarget) Name() string                  { return "sentinel" }
func (s *sentinelTarget) Accepts(eventName string) bool { return acceptsEvent(s.cfg.Events, eventName) }
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
	sig := sentinelSignature(len(body), dateStr, s.cfg.SharedKey, s.cfg.WorkspaceID)
	url := fmt.Sprintf("https://%s.ods.opinsights.azure.com/api/logs?api-version=2016-04-01", s.cfg.WorkspaceID)

	headers := map[string]string{
		"Authorization":        sig,
		"Log-Type":             logType,
		"x-ms-date":           dateStr,
		"time-generated-field": "TimeGenerated",
	}
	return postJSON(ctx, s.client, url, headers, payload)
}

func sentinelSignature(contentLength int, date, sharedKey, workspaceID string) string {
	stringToSign := fmt.Sprintf("POST\n%d\napplication/json\nx-ms-date:%s\n/api/logs", contentLength, date)
	keyBytes, _ := base64.StdEncoding.DecodeString(sharedKey)
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("SharedKey %s:%s", workspaceID, sig)
}
