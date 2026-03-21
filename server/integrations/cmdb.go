// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/daniel-alexander4/vigo/server/config"
)

// --- ServiceNow ---

type servicenowTarget struct {
	cfg    *config.ServiceNowIntegration
	client *http.Client
}

func (s *servicenowTarget) Name() string                  { return "servicenow" }
func (s *servicenowTarget) Accepts(eventName string) bool { return acceptsEvent(s.cfg.Events, eventName) }
func (s *servicenowTarget) Dispatch(ctx context.Context, e Event) error {
	payload := map[string]any{
		"short_description": fmt.Sprintf("[Vigo] %s: %s", e.Name, e.Hostname),
		"description":       fmt.Sprintf("Vigo detected %s on %s at %s.\n\n%s", e.Name, e.Hostname, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
		"urgency":           "2",
		"impact":            "2",
		"category":          "Software",
	}
	url := fmt.Sprintf("https://%s/api/now/table/incident", s.cfg.Instance)
	auth := basicAuth(s.cfg.Username + ":" + s.cfg.Password)
	headers := map[string]string{
		"Authorization": "Basic " + auth,
		"Accept":        "application/json",
	}
	return postJSON(ctx, s.client, url, headers, payload)
}

// --- Device42 ---

type device42Target struct {
	cfg    *config.Device42Integration
	client *http.Client
}

func (d *device42Target) Name() string                  { return "device42" }
func (d *device42Target) Accepts(eventName string) bool { return acceptsEvent(d.cfg.Events, eventName) }
func (d *device42Target) Dispatch(ctx context.Context, e Event) error {
	// Device42 REST API — create or update a device.
	payload := map[string]any{
		"name":       e.Hostname,
		"notes":      fmt.Sprintf("Vigo event: %s at %s\n%s", e.Name, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
		"tags":       "vigo-managed",
		"service_level": "Production",
	}
	url := strings.TrimSuffix(d.cfg.URL, "/") + "/api/1.0/devices/"
	auth := basicAuth(d.cfg.Username + ":" + d.cfg.Password)
	headers := map[string]string{"Authorization": "Basic " + auth}
	return postJSON(ctx, d.client, url, headers, payload)
}

// --- Snipe-IT ---

type snipeITTarget struct {
	cfg    *config.SnipeITIntegration
	client *http.Client
}

func (s *snipeITTarget) Name() string                  { return "snipeit" }
func (s *snipeITTarget) Accepts(eventName string) bool { return acceptsEvent(s.cfg.Events, eventName) }
func (s *snipeITTarget) Dispatch(ctx context.Context, e Event) error {
	// Snipe-IT REST API — create or update hardware asset.
	payload := map[string]any{
		"name":   e.Hostname,
		"notes":  fmt.Sprintf("Vigo event: %s at %s\n%s", e.Name, e.Timestamp.Format("2006-01-02 15:04:05 UTC"), e.Summary),
		"status_id": 2, // deployed
	}
	url := strings.TrimSuffix(s.cfg.URL, "/") + "/hardware"
	headers := map[string]string{
		"Authorization": "Bearer " + s.cfg.Token,
		"Accept":        "application/json",
	}
	return postJSON(ctx, s.client, url, headers, payload)
}
